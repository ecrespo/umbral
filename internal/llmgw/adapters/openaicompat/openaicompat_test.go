package openaicompat

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

// fakeServer is an OpenAI-compatible endpoint: a model list and a streamed chat completion.
type fakeServer struct {
	mu      sync.Mutex
	auth    []string
	body    map[string]any
	status  int
	models  string
	chunks  []string
	modelsN int
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.modelsN++
		f.mu.Unlock()
		if f.status != 0 {
			http.Error(w, `{"error":{"message":"busy"}}`, f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, f.models)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		_ = json.Unmarshal(raw, &f.body)
		f.mu.Unlock()
		if f.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range f.chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	return mux
}

func chunk(delta, finish string) string {
	f := "null"
	if finish != "" {
		f = `"` + finish + `"`
	}
	return `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` +
		delta + `,"finish_reason":` + f + `}]}`
}

// TestDiscoverModels_REQ_LLM_002: the adapter lists the models behind `/v1/models`, prefixed
// with the provider's id, local when the endpoint is on this machine, with the context
// window when the server gives one — and sends the key it was given as a bearer token.
func TestDiscoverModels_REQ_LLM_002(t *testing.T) {
	t.Parallel()

	f := &fakeServer{models: `{"object":"list","data":[
		{"id":"qwen3:4b","object":"model"},
		{"id":"gpt-oss:20b","object":"model","context_length":32768},
		{"id":"llama","object":"model","meta":{"n_ctx_train":8192}}]}`}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	p, err := New(Config{ID: "lms", BaseURL: srv.URL + "/v1", APIKey: domain.NewAPIKey("test-key")})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "lms" || !p.Local() {
		t.Errorf("id %q local %v, want lms and local: the endpoint is loopback", p.ID(), p.Local())
	}
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v, want 3", models)
	}
	for _, id := range []string{"lms/qwen3:4b", "lms/gpt-oss:20b", "lms/llama"} {
		m, ok := byID[id]
		if !ok || m.Provider != "lms" || !m.Local || !m.Caps.Tools {
			t.Errorf("%s = %+v, want a local lms model with tools", id, m)
		}
	}
	if byID["lms/gpt-oss:20b"].Caps.ContextWindow != 32768 || byID["lms/llama"].Caps.ContextWindow != 8192 {
		t.Errorf("context windows = %d, %d", byID["lms/gpt-oss:20b"].Caps.ContextWindow, byID["lms/llama"].Caps.ContextWindow)
	}
	if f.auth[0] != "Bearer test-key" {
		t.Errorf("Authorization = %q, want the bearer key", f.auth[0])
	}

	for _, base := range []string{"https://router.huggingface.co/v1", "http://192.168.1.10:1234/v1"} {
		if remote, _ := New(Config{ID: "hf", BaseURL: base}); remote.Local() {
			t.Errorf("%s is local: only loopback is (REQ-LLM-004)", base)
		}
	}
	for _, base := range []string{"http://localhost:1234/v1", "http://[::1]:8080/v1"} {
		if p, _ := New(Config{ID: "x", BaseURL: base}); !p.Local() {
			t.Errorf("%s is not local", base)
		}
	}

	f.status = http.StatusServiceUnavailable
	_, err = p.Models(context.Background())
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status != 503 || !pe.Retryable() {
		t.Errorf("a 503 model list = %v, want a retryable ProviderError", err)
	}
}

// TestStreamNormalized_REQ_LLM_001: a streamed completion comes out as the gateway's
// normalized events — text deltas, a complete tool call, usage, done — and the request that
// went out carries the model, the system prompt, the conversation and the tools.
func TestStreamNormalized_REQ_LLM_001(t *testing.T) {
	t.Parallel()

	f := &fakeServer{chunks: []string{
		chunk(`{"role":"assistant","content":"Hel"}`, ""),
		chunk(`{"content":"lo"}`, ""),
		chunk(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}`, ""),
		chunk(`{}`, "tool_calls"),
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
	}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	p, err := New(Config{ID: "lms", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(context.Background(), domain.Request{
		Model:  "qwen3:4b",
		System: "You are terse.",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "read a.go"},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "call_0", Name: "list", Input: `{}`}}},
			{Role: domain.RoleTool, ToolCallID: "call_0", Text: "a.go"},
		},
		Tools: []domain.ToolSpec{{
			Name: "read_file", Description: "Read a file",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
		MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var calls []domain.ToolCall
	var usage domain.Usage
	var done []string
	for ev, err := range stream {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		switch ev.Kind {
		case domain.EventTextDelta:
			text.WriteString(ev.Text)
		case domain.EventToolCall:
			calls = append(calls, ev.ToolCall)
		case domain.EventUsage:
			usage = ev.Usage
		case domain.EventDone:
			done = append(done, ev.FinishReason)
		}
	}
	if text.String() != "Hello" {
		t.Errorf("text = %q, want Hello", text.String())
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read_file" || calls[0].Input != `{"path":"a.go"}` {
		t.Errorf("tool calls = %+v", calls)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 5 {
		t.Errorf("usage = %+v, want 12 in / 5 out", usage)
	}
	if len(done) != 1 || done[0] != "tool_calls" {
		t.Errorf("done = %v, want one done with tool_calls", done)
	}

	body := f.body
	if body["model"] != "qwen3:4b" || body["stream"] != true {
		t.Errorf("request model/stream = %v/%v", body["model"], body["stream"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %v, want system + 3", msgs)
	}
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Errorf("roles = %v", roles)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || !strings.Contains(fmt.Sprint(tools[0]), "read_file") {
		t.Errorf("tools = %v", tools)
	}
}

// TestARefusedCallIsAProviderError: a 429 before the stream starts is a retryable
// ProviderError with its status, so the router can move to the next candidate (T-F1-07).
func TestARefusedCallIsAProviderError(t *testing.T) {
	t.Parallel()

	f := &fakeServer{status: http.StatusTooManyRequests}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	p, _ := New(Config{ID: "lms", BaseURL: srv.URL + "/v1"})
	stream, err := p.Stream(context.Background(), domain.Request{
		Model: "m", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err == nil {
		for _, e := range stream {
			err = e
			if err != nil {
				break
			}
		}
	}
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status != 429 || !pe.Retryable() || pe.Provider != "lms" {
		t.Errorf("err = %v, want a retryable ProviderError with 429", err)
	}
}

// TestAKeyNeverPrints: neither the configuration nor the provider prints its key under any
// verb (Tech Design §5.1).
func TestAKeyNeverPrints(t *testing.T) {
	t.Parallel()

	cfg := Config{ID: "hf", BaseURL: "https://router.huggingface.co/v1", APIKey: domain.NewAPIKey("hf_never_printed")}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, v := range []any{cfg, p} {
			if out := fmt.Sprintf(f, v); strings.Contains(out, "never_printed") {
				t.Errorf("%s of %T printed the key", f, v)
			}
		}
	}
}

func TestConfigIsChecked(t *testing.T) {
	t.Parallel()

	for _, c := range []Config{{BaseURL: "http://x"}, {ID: "x"}, {ID: "x", BaseURL: "::not a url"}} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) accepted", c)
		}
	}
}
