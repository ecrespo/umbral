package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/llmgw"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/ollama"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// TestEveryProviderTypeGetsItsAdapter_REQ_LLM_001: each type of models.toml maps to its adapter; a
// provider down for its credential gets none, so nothing can call it (REQ-SEC-008); a type
// with no adapter yet is reported as such rather than hidden; a base URL no adapter accepts
// is down with invalid_config.
func TestEveryProviderTypeGetsItsAdapter_REQ_LLM_001(t *testing.T) {
	t.Parallel()

	models := config.Models{Providers: []config.Provider{
		{ID: "lms", Type: "lmstudio", BaseURL: "http://127.0.0.1:1234/v1"},
		{ID: "llama", Type: "llamacpp", BaseURL: "http://127.0.0.1:8080/v1"},
		{ID: "compat", Type: "openai-compat", BaseURL: "https://example.com/v1"},
		{ID: "or", Type: "openrouter", BaseURL: "https://openrouter.ai/api/v1"},
		{ID: "hf", Type: "openai-compat", BaseURL: "https://router.huggingface.co/v1"},
		{ID: "ollama", Type: "ollama", BaseURL: "http://127.0.0.1:11434"},
		{ID: "yz", Type: "yzma", LibPath: "/x", ModelsDir: "/y"},
		{ID: "bad", Type: "openai-compat", BaseURL: "ftp://example.com"},
	}}
	resolved := []secdomain.ResolvedCredential{
		{ProviderID: "or", Health: secdomain.HealthDegraded, Reason: secdomain.ReasonEnvSecret, Secret: secdomain.NewSecret("k")},
		{ProviderID: "hf", Health: secdomain.HealthDown, Reason: secdomain.ReasonKeyringUnavailable},
		{ProviderID: "yz", Health: secdomain.HealthDegraded, Reason: secdomain.ReasonEnvSecret},
	}
	entries := buildEntries(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, models, resolved)
	byID := map[string]struct {
		adapter bool
		health  llmdomain.Health
		reason  string
	}{}
	for _, e := range entries {
		byID[e.ID] = struct {
			adapter bool
			health  llmdomain.Health
			reason  string
		}{e.Provider != nil, e.Health, e.Reason}
	}
	for id, want := range map[string]struct {
		adapter bool
		health  llmdomain.Health
		reason  string
	}{
		"lms":    {true, llmdomain.HealthUnknown, ""},
		"llama":  {true, llmdomain.HealthUnknown, ""},
		"compat": {true, llmdomain.HealthUnknown, ""},
		"or":     {true, llmdomain.HealthDegraded, secdomain.ReasonEnvSecret},
		"hf":     {false, llmdomain.HealthDown, secdomain.ReasonKeyringUnavailable},
		"ollama": {true, llmdomain.HealthUnknown, ""},
		"yz":     {false, llmdomain.HealthUnknown, llmdomain.ReasonNoAdapter},
		"bad":    {false, llmdomain.HealthDown, llmdomain.ReasonInvalidConfig},
	} {
		if byID[id] != want {
			t.Errorf("%s = %+v, want %+v", id, byID[id], want)
		}
	}
	if len(entries) != len(models.Providers) {
		t.Errorf("%d entries for %d providers", len(entries), len(models.Providers))
	}
}

// TestADaemonDiscoversModelsAtStart_REQ_LLM_002: a daemon started with an OpenAI-compatible
// provider in models.toml lists that provider's models through model.list without being
// asked to refresh, and `refresh: true` asks the provider again.
func TestADaemonDiscoversModelsAtStart_REQ_LLM_002(t *testing.T) {
	var asked atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"qwen3:4b"},{"id":"gpt-oss:20b","context_length":32768}]}`)
	}))
	defer fake.Close()

	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "models.toml"),
		"[[providers]]\nid = \"lms\"\ntype = \"lmstudio\"\nbase_url = \""+fake.URL+"/v1\"\n")
	startDaemonLoggingTo(t, bin, rt, io.Discard)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	type model struct {
		ID, Provider, Health string
		Local                bool
	}
	var list struct{ Items []model }
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := c.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatalf("model.list: %v", err)
		}
		if len(list.Items) == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(list.Items) != 2 || list.Items[0].ID != "lms/gpt-oss:20b" || list.Items[0].Health != "ok" || !list.Items[0].Local {
		t.Fatalf("model.list = %+v, want the two models of lms, ok and local", list.Items)
	}
	before := asked.Load()
	if err := c.Call(ctx, "model.list", map[string]any{"refresh": true}, &list); err != nil {
		t.Fatal(err)
	}
	if asked.Load() != before+1 {
		t.Errorf("refresh asked the provider %d more times, want 1", asked.Load()-before)
	}
}

// TestOfflineReachesTheCatalog_REQ_LLM_004: `router.offline` in models.toml is what the
// catalog obeys: a remote provider is not offered while it is set. The context is cancelled
// and no egress log is wired, so nothing here can reach the network either way.
func TestOfflineReachesTheCatalog_REQ_LLM_004(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g, err := newGateway(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), nopStore{}, nil, nopUsage{})
	if err != nil {
		t.Fatal(err)
	}
	models := config.Models{Providers: []config.Provider{
		{ID: "remote", Type: "openai-compat", BaseURL: "https://example.invalid/v1"},
	}}
	g.configure(models, nil)
	if _, ok := g.catalog.Provider("remote"); !ok {
		t.Fatal("a remote provider is not offered while online")
	}
	models.Router.Offline = true
	g.configure(models, nil)
	if _, ok := g.catalog.Provider("remote"); ok {
		t.Error("a remote provider is offered with router.offline = true")
	}
}

type nopStore struct{}

func (nopStore) Replace(context.Context, string, []llmdomain.Model) error  { return nil }
func (nopStore) SetHealth(context.Context, string, llmdomain.Health) error { return nil }
func (nopStore) List(context.Context) ([]llmdomain.Model, error)           { return nil, nil }
func (nopStore) Retain(context.Context, []string) error                    { return nil }

type nopUsage struct{}

func (nopUsage) Record(context.Context, llmdomain.UsageRecord) error { return nil }

func TestTheGatewayNeedsAUsageLog_REQ_LLM_005(t *testing.T) {
	t.Parallel()

	if _, err := newGateway(context.Background(), slog.New(slog.DiscardHandler), nopStore{}, nil, nil); err == nil {
		t.Error("a gateway that records no model call was built")
	}
}

// memModels is the model store in memory.
type memModels struct {
	mu   sync.Mutex
	rows []llmdomain.Model
}

func (m *memModels) Replace(_ context.Context, _ string, models []llmdomain.Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append([]llmdomain.Model(nil), models...)
	return nil
}
func (*memModels) SetHealth(context.Context, string, llmdomain.Health) error { return nil }
func (*memModels) Retain(context.Context, []string) error                    { return nil }
func (m *memModels) List(context.Context) ([]llmdomain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llmdomain.Model(nil), m.rows...), nil
}

type memEgress struct {
	mu   sync.Mutex
	recs []llmdomain.EgressRecord
}

func (e *memEgress) Record(_ context.Context, rec llmdomain.EgressRecord) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recs = append(e.recs, rec)
	return nil
}

// toServer sends every request to srv whatever host it names, so a remote provider can be
// exercised without leaving the machine.
type toServer struct{ srv *httptest.Server }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(t.srv.URL, "http://")
	return http.DefaultTransport.RoundTrip(r)
}

// TestEgressLoggedForRemote_REQ_SEC_002: a call the router sends to a remote provider leaves
// one egress row with the host, the provider, the thread, the bytes and the SHA-256 of
// exactly the payload that left — which is the redacted one (REQ-SEC-001).
func TestEgressLoggedForRemote_REQ_SEC_002(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var chat []byte
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[{"name":"gpt-oss:20b","details":{"context_length":131072},"capabilities":["completion","tools"]}]}`)
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		chat = body
		mu.Unlock()
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":1}`+"\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	egress := &memEgress{}
	provider, err := ollama.New(ollama.Config{
		ID: "gpu", BaseURL: "http://gpu-box.example:11434", Egress: egress,
		HTTPClient: &http.Client{Transport: toServer{srv}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	g, err := newGateway(ctx, slog.New(slog.DiscardHandler), &memModels{}, egress, nopUsage{})
	if err != nil {
		t.Fatal(err)
	}
	g.catalog.Configure(false, []llmgw.Entry{{ID: "gpu", Provider: provider, Health: llmdomain.HealthOK}})
	g.router.Configure(map[string][]string{"code": {"gpu/gpt-oss:20b"}})
	if err := g.catalog.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	token := "ghp_" + strings.Repeat("x", 36)
	stream, err := g.router.Stream(ctx, llmgw.Call{ThreadID: "thr_1", Class: "code", Request: llmdomain.Request{
		Messages: []llmdomain.Message{{Role: llmdomain.RoleUser, Text: "push with " + token}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	sent := chat
	mu.Unlock()
	if strings.Contains(string(sent), token) || !strings.Contains(string(sent), "[REDACTED:github_token]") {
		t.Fatalf("the payload that left was not redacted: %s", sent)
	}
	if len(egress.recs) != 2 {
		t.Fatalf("egress rows = %+v, want discovery and the call", egress.recs)
	}
	row := egress.recs[1]
	sum := sha256.Sum256(sent)
	if row.Host != "gpu-box.example" || row.Provider != "gpu" || row.ThreadID != "thr_1" ||
		row.Bytes != int64(len(sent)) || row.PayloadSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("row = %+v, want host, provider, thread, %d bytes and the payload's hash", row, len(sent))
	}
}

type memUsageLog struct {
	mu   sync.Mutex
	recs []llmdomain.UsageRecord
}

func (u *memUsageLog) Record(_ context.Context, rec llmdomain.UsageRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.recs = append(u.recs, rec)
	return nil
}

// TestAServerThatHoldsItsHeadersTimesOut_REQ_LLM_003: an Ollama loading a model sends no
// response headers, so the real adapter is still inside Stream when the first-token timeout
// fires. The call must move to the next candidate all the same, and record a timeout.
func TestAServerThatHoldsItsHeadersTimesOut_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	serve := func(hold bool) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"models":[{"name":"m","details":{"context_length":8192},"capabilities":["completion"]}]}`)
		})
		mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
			if hold {
				// The server notices the client leaving only once the body is read.
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`+"\n")
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv
	}
	loading, err := ollama.New(ollama.Config{ID: "loading", BaseURL: serve(true).URL})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := ollama.New(ollama.Config{ID: "ready", BaseURL: serve(false).URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	catalog := llmgw.NewCatalog(&memModelsByProvider{rows: map[string][]llmdomain.Model{}}, slog.New(slog.DiscardHandler))
	catalog.Configure(false, []llmgw.Entry{
		{ID: "loading", Provider: loading, Health: llmdomain.HealthOK},
		{ID: "ready", Provider: ready, Health: llmdomain.HealthOK},
	})
	if err := catalog.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	usage := &memUsageLog{}
	router, err := llmgw.NewRouter(catalog, llmgw.RouterConfig{
		Classes: map[string][]string{"code": {"loading/m", "ready/m"}},
		Redact:  redact, Usage: usage, FirstTokenLocal: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := router.Stream(ctx, llmgw.Call{Class: "code", Request: llmdomain.Request{
		Messages: []llmdomain.Message{{Role: llmdomain.RoleUser, Text: "hi"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev, err := range stream {
		if err != nil {
			t.Fatalf("the call failed instead of falling back: %v", err)
		}
		text += ev.Text
	}
	if text != "ok" {
		t.Errorf("text = %q, want the ready candidate's answer", text)
	}
	usage.mu.Lock()
	defer usage.mu.Unlock()
	if len(usage.recs) != 2 || usage.recs[0].Status != llmdomain.UsageTimeout || usage.recs[1].Status != llmdomain.UsageOK {
		t.Errorf("usage = %+v, want a timeout then ok", usage.recs)
	}
}

// memModelsByProvider is the model store in memory, per provider.
type memModelsByProvider struct {
	mu   sync.Mutex
	rows map[string][]llmdomain.Model
}

func (m *memModelsByProvider) Replace(_ context.Context, provider string, models []llmdomain.Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[provider] = append([]llmdomain.Model(nil), models...)
	return nil
}

func (*memModelsByProvider) SetHealth(context.Context, string, llmdomain.Health) error { return nil }

func (*memModelsByProvider) Retain(context.Context, []string) error { return nil }

func (m *memModelsByProvider) List(context.Context) ([]llmdomain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []llmdomain.Model
	for _, ms := range m.rows {
		out = append(out, ms...)
	}
	return out, nil
}
