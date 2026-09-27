package openrouter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

const models = `{"data":[
 {"id":"moonshotai/kimi-k2","context_length":131072,
  "pricing":{"prompt":"0.00000057","completion":"0.0000023"},
  "architecture":{"input_modalities":["text"]},
  "supported_parameters":["tools","tool_choice","temperature"]},
 {"id":"openai/gpt-oss-120b","context_length":131072,
  "pricing":{"prompt":"0","completion":"0"},
  "architecture":{"input_modalities":["text","image"]},
  "supported_parameters":["tools","reasoning","structured_outputs"]},
 {"id":"broken/pricing","context_length":4096,"pricing":{"prompt":"n/a","completion":"-1"}}]}`

func server(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, models)
	})
	mux.HandleFunc("POST /api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
			`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDiscoverOpenRouterModels_REQ_LLM_002: OpenRouter's model list carries prices in USD
// per token and each model's parameters; the catalog gets micro-USD per million tokens
// (Art. 6) and the capabilities those parameters imply. A price it cannot read is 0.
func TestDiscoverOpenRouterModels_REQ_LLM_002(t *testing.T) {
	t.Parallel()

	var seen []string
	srv := server(t, &seen)
	p, err := New(Config{ID: "or", BaseURL: srv.URL + "/api/v1", APIKey: domain.NewAPIKey("or-key")})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "or" || p.Local() {
		t.Errorf("id %q local %v, want or, remote", p.ID(), p.Local())
	}
	got, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Model{}
	for _, m := range got {
		byID[m.ID] = m
	}
	kimi := byID["or/moonshotai/kimi-k2"]
	if kimi.PriceInMicroUSDPerMTok != 570_000 || kimi.PriceOutMicroUSDPerMTok != 2_300_000 {
		t.Errorf("kimi prices = %d / %d µUSD/Mtok, want 570000 / 2300000", kimi.PriceInMicroUSDPerMTok, kimi.PriceOutMicroUSDPerMTok)
	}
	if !kimi.Caps.Tools || kimi.Caps.Reasoning || kimi.Caps.Vision || kimi.Caps.JSONSchema || kimi.Caps.ContextWindow != 131072 {
		t.Errorf("kimi caps = %+v", kimi.Caps)
	}
	if gpt := byID["or/openai/gpt-oss-120b"]; !gpt.Caps.Reasoning || !gpt.Caps.Vision || !gpt.Caps.JSONSchema {
		t.Errorf("gpt-oss caps = %+v", gpt.Caps)
	}
	if b := byID["or/broken/pricing"]; b.PriceInMicroUSDPerMTok != 0 || b.PriceOutMicroUSDPerMTok != 0 {
		t.Errorf("unreadable prices = %d / %d, want 0", b.PriceInMicroUSDPerMTok, b.PriceOutMicroUSDPerMTok)
	}
	if len(seen) == 0 || seen[0] != "/api/v1/models Bearer or-key" {
		t.Errorf("requests = %v", seen)
	}
}

// TestOpenRouterStreamsThroughTheConfiguredBaseURL_REQ_LLM_001: Fantasy's OpenRouter provider has its
// URL built in; the adapter sends its calls to the base_url models.toml gives instead, key
// included, and the stream comes out normalized.
func TestOpenRouterStreamsThroughTheConfiguredBaseURL_REQ_LLM_001(t *testing.T) {
	t.Parallel()

	var seen []string
	srv := server(t, &seen)
	p, err := New(Config{ID: "or", BaseURL: srv.URL + "/api/v1", APIKey: domain.NewAPIKey("or-key")})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(context.Background(), domain.Request{
		Model: "moonshotai/kimi-k2", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var kinds []domain.EventKind
	for ev, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
		text.WriteString(ev.Text)
	}
	if text.String() != "ok" || kinds[len(kinds)-1] != domain.EventDone {
		t.Errorf("text %q, events %v", text.String(), kinds)
	}
	if len(seen) != 1 || seen[0] != "/api/v1/chat/completions Bearer or-key" {
		t.Errorf("requests = %v, want the call at the configured base URL", seen)
	}
}

// TestRewriteRefusesOtherURLs: only OpenRouter's own URL is rewritten; anything else — another
// host, or a path that merely starts with the same letters — is refused, since the request
// carries the key.
func TestRewriteRefusesOtherURLs(t *testing.T) {
	t.Parallel()

	rt := rewrite{from: "https://openrouter.ai/api/v1", to: "http://127.0.0.1:9/v1", next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(r.URL.String())), Request: r}, nil
	})}
	for in, want := range map[string]string{
		"https://openrouter.ai/api/v1/models?x=1": "http://127.0.0.1:9/v1/models?x=1",
		"https://openrouter.ai/api/v1":            "http://127.0.0.1:9/v1",
		"https://example.com/api/v1/models":       "",
		"https://openrouter.ai/api/v1evil/models": "",
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, in, nil)
		resp, err := rt.RoundTrip(req)
		if want == "" {
			if err == nil {
				t.Errorf("%s was let through", in)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != want {
			t.Errorf("%s went to %s, want %s", in, body, want)
		}
	}
	if _, err := New(Config{ID: "or", BaseURL: "ftp://x"}); err == nil {
		t.Error("a non-http base URL was accepted")
	}
	if _, err := New(Config{BaseURL: DefaultBaseURL}); err == nil {
		t.Error("a provider without an id was accepted")
	}
}

// TestAKeyNeverPrints: an adapter's configuration can reach a log line by accident; its key
// does not (Tech Design §5.1).
func TestAKeyNeverPrints(t *testing.T) {
	t.Parallel()

	cfg := Config{ID: "or", BaseURL: DefaultBaseURL, APIKey: domain.NewAPIKey("sk-or-v1-never-printed")}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, v := range []any{cfg, p, cfg.APIKey} {
			if out := fmt.Sprintf(f, v); strings.Contains(out, "never-printed") {
				t.Errorf("%s of %T printed the key", f, v)
			}
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
