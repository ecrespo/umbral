package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/config"
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
	g := newGateway(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), nopStore{}, nil)
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
