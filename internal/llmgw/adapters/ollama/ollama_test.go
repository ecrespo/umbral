package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

type fakeOllama struct {
	mu     sync.Mutex
	bodies []map[string]any
	lines  []string
	status int
}

const tags = `{"models":[
 {"name":"gpt-oss:20b","details":{"context_length":131072},"capabilities":["completion","tools","thinking"]},
 {"name":"qwen3:4b","details":{"context_length":8192},"capabilities":["completion","tools","thinking","vision"]},
 {"name":"nomic-embed-text:latest","details":{"context_length":2048},"capabilities":["embedding"]},
 {"name":"old:latest","details":{}}]}`

func (f *fakeOllama) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		_, _ = io.WriteString(w, tags)
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"error":"model is loading"}`)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, l := range f.lines {
			_, _ = fmt.Fprintln(w, l)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeOllama) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[len(f.bodies)-1]
}

var toolCallStream = []string{
	`{"model":"m","message":{"role":"assistant","content":"","thinking":"I should"},"done":false}`,
	`{"model":"m","message":{"role":"assistant","content":"Let me "},"done":false}`,
	`{"model":"m","message":{"role":"assistant","content":"look."},"done":false}`,
	`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"index":0,"name":"read_file","arguments":{"path":"a.go"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":133,"eval_count":42}`,
}

func drain(t *testing.T, p *Provider, req domain.Request) ([]domain.Event, error) {
	t.Helper()
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		return nil, err
	}
	var out []domain.Event
	for ev, err := range stream {
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// TestOllamaSendsNumCtx_REQ_LLM_006: every request carries a num_ctx — the one models.toml
// configures for the provider, or, when none is, 32768 capped at the model's own context
// window from discovery — so Ollama's short default never applies. keep_alive goes at the top
// level and the other options inside `options`.
func TestOllamaSendsNumCtx_REQ_LLM_006(t *testing.T) {
	t.Parallel()

	f := &fakeOllama{lines: toolCallStream}
	srv := f.server(t)
	req := domain.Request{Model: "gpt-oss:20b", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	configured, err := New(Config{ID: "ollama", BaseURL: srv.URL, Options: map[string]any{
		"num_ctx": int64(16384), "keep_alive": "30m", "temperature": 0.2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := drain(t, configured, req); err != nil {
			t.Fatal(err)
		}
		body := f.last()
		opts, _ := body["options"].(map[string]any)
		if opts["num_ctx"] != float64(16384) || opts["temperature"] != 0.2 || body["keep_alive"] != "30m" {
			t.Errorf("request = %v, want num_ctx 16384, temperature 0.2 and keep_alive 30m", body)
		}
		if _, leaked := opts["keep_alive"]; leaked {
			t.Error("keep_alive was sent inside options")
		}
	}

	limited := req
	limited.MaxOutputTokens = 256
	if _, err := drain(t, configured, limited); err != nil {
		t.Fatal(err)
	}
	if opts, _ := f.last()["options"].(map[string]any); opts["num_predict"] != float64(256) {
		t.Errorf("num_predict = %v, want 256", opts["num_predict"])
	}

	unconfigured, _ := New(Config{ID: "ollama", BaseURL: srv.URL})
	if _, err := drain(t, unconfigured, req); err != nil {
		t.Fatal(err)
	}
	if opts, _ := f.last()["options"].(map[string]any); opts["num_ctx"] != float64(DefaultNumCtx) {
		t.Errorf("num_ctx without discovery = %v, want %d", opts["num_ctx"], DefaultNumCtx)
	}
	if _, err := unconfigured.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, unconfigured, domain.Request{Model: "qwen3:4b", Messages: req.Messages}); err != nil {
		t.Fatal(err)
	}
	if opts, _ := f.last()["options"].(map[string]any); opts["num_ctx"] != float64(8192) {
		t.Errorf("num_ctx for an 8K model = %v, want 8192: never more than the model has", opts["num_ctx"])
	}
}

// TestOllamaStreamsNormalized_REQ_LLM_001: `/api/chat`'s NDJSON comes out as the gateway's
// events — thinking as reasoning, text, a complete tool call, usage from the eval counts, and
// done with `tool_calls` when the model called one — and the request carries the system
// prompt, the conversation with tool results by name, the tools, `think` and `format`.
func TestOllamaStreamsNormalized_REQ_LLM_001(t *testing.T) {
	t.Parallel()

	f := &fakeOllama{lines: toolCallStream}
	srv := f.server(t)
	p, _ := New(Config{ID: "ollama", BaseURL: srv.URL})
	events, err := drain(t, p, domain.Request{
		Model: "gpt-oss:20b", System: "Be terse.",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "read a.go"},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "c0", Name: "list", Input: `{"dir":"."}`}}},
			{Role: domain.RoleTool, ToolCallID: "c0", ToolName: "list", Text: "a.go"},
		},
		Tools:          []domain.ToolSpec{{Name: "read_file", Description: "Read", InputSchema: map[string]any{"type": "object"}}},
		Reasoning:      "low",
		ResponseSchema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reasoning, text strings.Builder
	var calls []domain.ToolCall
	var usage domain.Usage
	var done string
	for _, ev := range events {
		switch ev.Kind {
		case domain.EventReasoningDelta:
			reasoning.WriteString(ev.Text)
		case domain.EventTextDelta:
			text.WriteString(ev.Text)
		case domain.EventToolCall:
			calls = append(calls, ev.ToolCall)
		case domain.EventUsage:
			usage = ev.Usage
		case domain.EventDone:
			done = ev.FinishReason
		}
	}
	if reasoning.String() != "I should" || text.String() != "Let me look." {
		t.Errorf("reasoning %q, text %q", reasoning.String(), text.String())
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read_file" || calls[0].Input != `{"path":"a.go"}` {
		t.Errorf("calls = %+v", calls)
	}
	if usage.InputTokens != 133 || usage.OutputTokens != 42 || done != "tool_calls" {
		t.Errorf("usage %+v, done %q", usage, done)
	}
	if events[len(events)-1].Kind != domain.EventDone {
		t.Errorf("the stream did not end with done: %+v", events)
	}

	body := f.last()
	if body["model"] != "gpt-oss:20b" || body["stream"] != true || body["think"] != "low" {
		t.Errorf("model/stream/think = %v/%v/%v", body["model"], body["stream"], body["think"])
	}
	if fmt.Sprint(body["format"]) != "map[type:object]" {
		t.Errorf("format = %v", body["format"])
	}
	msgs := body["messages"].([]any)
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Errorf("roles = %v", roles)
	}
	assistant := msgs[2].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if call["name"] != "list" || fmt.Sprint(call["arguments"]) != "map[dir:.]" {
		t.Errorf("assistant tool call = %v, want arguments as an object", call)
	}
	if tool := msgs[3].(map[string]any); tool["tool_name"] != "list" || tool["content"] != "a.go" {
		t.Errorf("tool result = %v", tool)
	}
	if tools := body["tools"].([]any); len(tools) != 1 || !strings.Contains(fmt.Sprint(tools[0]), "read_file") {
		t.Errorf("tools = %v", tools)
	}

	for think, want := range map[string]any{"on": true, "off": false} {
		_, _ = drain(t, p, domain.Request{Model: "m", Reasoning: think, Messages: []domain.Message{{Role: domain.RoleUser, Text: "x"}}})
		if f.last()["think"] != want {
			t.Errorf("reasoning %q sent think = %v, want %v", think, f.last()["think"], want)
		}
	}
	_, _ = drain(t, p, domain.Request{Model: "m", Messages: []domain.Message{{Role: domain.RoleUser, Text: "x"}}})
	if _, sent := f.last()["think"]; sent {
		t.Error("think was sent when the request left it to the model")
	}
}

// TestOllamaDiscovers_REQ_LLM_002: `/api/tags` lists the models with their context window and
// the capabilities Ollama reports; an embedding model is listed without tools, and an older
// Ollama that reports no capabilities is assumed to take tools.
func TestOllamaDiscovers_REQ_LLM_002(t *testing.T) {
	t.Parallel()

	f := &fakeOllama{}
	srv := f.server(t)
	p, _ := New(Config{ID: "ollama", BaseURL: srv.URL})
	if !p.Local() || p.ID() != "ollama" {
		t.Errorf("id %q local %v", p.ID(), p.Local())
	}
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]domain.Model{}
	for _, m := range models {
		by[m.ID] = m
	}
	if g := by["ollama/gpt-oss:20b"]; !g.Local || !g.Caps.Tools || !g.Caps.Reasoning || g.Caps.Vision || !g.Caps.JSONSchema || g.Caps.ContextWindow != 131072 {
		t.Errorf("gpt-oss = %+v", g)
	}
	if q := by["ollama/qwen3:4b"]; !q.Caps.Vision {
		t.Errorf("qwen3 = %+v, want vision", q)
	}
	if e := by["ollama/nomic-embed-text:latest"]; e.Caps.Tools || e.Caps.JSONSchema {
		t.Errorf("embedding model = %+v, want no tools", e)
	}
	if o := by["ollama/old:latest"]; !o.Caps.Tools || o.Caps.ContextWindow != 0 {
		t.Errorf("old = %+v, want tools assumed and no window", o)
	}

	f.status = http.StatusInternalServerError
	_, err = p.Models(context.Background())
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status != 500 {
		t.Errorf("a 500 = %v", err)
	}
}

// TestOllamaErrorsAreProviderErrors: a refused call carries its status for the router, and an
// error Ollama writes into the stream ends it with that error.
func TestOllamaErrorsAreProviderErrors(t *testing.T) {
	t.Parallel()

	f := &fakeOllama{status: http.StatusServiceUnavailable}
	srv := f.server(t)
	p, _ := New(Config{ID: "ollama", BaseURL: srv.URL})
	req := domain.Request{Model: "m", Messages: []domain.Message{{Role: domain.RoleUser, Text: "x"}}}
	_, err := drain(t, p, req)
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status != 503 || !pe.Retryable() || !strings.Contains(err.Error(), "model is loading") ||
		strings.Contains(err.Error(), `{"error"`) {
		t.Errorf("a 503 = %v", err)
	}

	f.status = 0
	f.lines = []string{`{"message":{"role":"assistant","content":"a"},"done":false}`, `{"error":"out of memory"}`}
	events, err := drain(t, p, req)
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "out of memory") || len(events) != 1 {
		t.Errorf("an in-stream error = %v after %+v", err, events)
	}

	f.lines = []string{`{"message":{"role":"assistant","content":"a"},"done":false}`}
	if _, err := drain(t, p, req); !errors.As(err, &pe) {
		t.Errorf("a stream cut before done = %v, want a ProviderError", err)
	}
	f.lines = []string{`not json`}
	if _, err := drain(t, p, req); err == nil {
		t.Error("a malformed line was accepted")
	}
	for _, c := range []Config{{BaseURL: "http://x"}, {ID: "x", BaseURL: "nope"}} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) accepted", c)
		}
	}
}

type recordingLog struct {
	mu   sync.Mutex
	recs []domain.EgressRecord
	fail bool
}

func (l *recordingLog) Record(_ context.Context, rec domain.EgressRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail {
		return errors.New("disk full")
	}
	l.recs = append(l.recs, rec)
	return nil
}

// toFake sends every request to srv whatever host it names, so a remote base URL can be
// tested without leaving the machine.
type toFake struct{ srv *httptest.Server }

func (t toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(t.srv.URL, "http://")
	return http.DefaultTransport.RoundTrip(r)
}

// TestOllamaRemoteRequestsAreLogged_REQ_SEC_002: an Ollama that is not on this machine is
// reached only through the egress log: discovery and chat each leave a row with the thread,
// and a row that cannot be written stops the request.
func TestOllamaRemoteRequestsAreLogged_REQ_SEC_002(t *testing.T) {
	t.Parallel()

	f := &fakeOllama{lines: toolCallStream}
	srv := f.server(t)
	log := &recordingLog{}
	p, err := New(Config{
		ID: "gpu-box", BaseURL: "http://ollama.example:11434", Egress: log,
		HTTPClient: &http.Client{Transport: toFake{srv}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Local() {
		t.Error("a remote Ollama reported local")
	}
	if _, err := p.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(domain.WithThread(context.Background(), "th_1"), domain.Request{
		Model: "gpt-oss:20b", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(log.recs) != 2 {
		t.Fatalf("egress rows = %+v, want discovery and chat", log.recs)
	}
	if r := log.recs[0]; r.Host != "ollama.example" || r.Provider != "gpu-box" || r.ThreadID != "" {
		t.Errorf("discovery row = %+v", r)
	}
	if r := log.recs[1]; r.ThreadID != "th_1" || r.Bytes == 0 || len(r.PayloadSHA256) != 64 {
		t.Errorf("chat row = %+v, want the thread, the size and the hash", r)
	}

	log.fail = true
	sent := len(f.bodies)
	if _, err := p.Stream(context.Background(), domain.Request{Model: "gpt-oss:20b"}); err == nil {
		t.Error("a request whose egress row failed was sent")
	}
	if len(f.bodies) != sent {
		t.Error("the server received a request the egress log refused")
	}
}

// TestOllamaRefusesABadNumCtx_REQ_LLM_006: a configured num_ctx that is not a positive integer
// is refused when the provider is built, so it can never reach Ollama.
func TestOllamaRefusesABadNumCtx_REQ_LLM_006(t *testing.T) {
	t.Parallel()

	for _, v := range []any{int64(0), int64(-1), "32768", 1.5, nil} {
		if _, err := New(Config{ID: "o", BaseURL: "http://127.0.0.1:9", Options: map[string]any{"num_ctx": v}}); err == nil {
			t.Errorf("num_ctx %#v accepted", v)
		}
	}
	for _, v := range []any{int64(4096), 8192, 16384.0} {
		if _, err := New(Config{ID: "o", BaseURL: "http://127.0.0.1:9", Options: map[string]any{"num_ctx": v}}); err != nil {
			t.Errorf("num_ctx %#v refused: %v", v, err)
		}
	}
}
