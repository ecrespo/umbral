package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeConfig answers config.get and config.reload with what the test sets.
type fakeConfig struct {
	view      ConfigView
	reloadErr error
	reloads   int
}

func (f *fakeConfig) Get(context.Context) (ConfigView, error) { return f.view, nil }

func (f *fakeConfig) Reload(context.Context) (ConfigView, error) {
	f.reloads++
	if f.reloadErr != nil {
		return ConfigView{}, f.reloadErr
	}
	return f.view, nil
}

var sampleView = ConfigView{
	Settings: ConfigSettings{MaxMessageBytes: 4 << 20, AllowEnv: true},
	Providers: []ConfigProvider{
		{
			ID: "hf", Type: "openai-compat", BaseURL: "https://router.huggingface.co/v1",
			Credential: "keyring:umbral/hf_token", Health: "down", Reason: "keyring_unavailable",
		},
		{ID: "ollama", Type: "ollama", BaseURL: "http://127.0.0.1:11434", Health: "unknown"},
	},
	Rejected: []ConfigRejection{},
}

// TestConfigGetAndReloadReachTheWire: both methods answer with the view the daemon holds —
// credential references, never values — and a reload that refuses entries answers
// CONFIG_INVALID with one detail per entry (API Spec §5.28, REQ-SEC-004). `umb` is not
// given the namespace (API Spec §2).
func TestConfigGetAndReloadReachTheWire(t *testing.T) {
	t.Parallel()

	cfg := &fakeConfig{view: sampleView}
	s := testServerWithConfig(t, Config{Configuration: cfg})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	var got ConfigView
	decodeResult(t, c.call(2, "config.get", nil), &got)
	if len(got.Providers) != 2 || got.Providers[0].Reason != "keyring_unavailable" ||
		got.Providers[0].Credential != "keyring:umbral/hf_token" || !got.Settings.AllowEnv {
		t.Errorf("config.get = %+v", got)
	}

	decodeResult(t, c.call(3, "config.reload", nil), &got)
	if cfg.reloads != 1 {
		t.Errorf("config.reload reached the service %d times, want 1", cfg.reloads)
	}

	cfg.reloadErr = ConfigInvalidError("provider entries were rejected",
		ErrorField{Field: "providers.leaky.api_key", Issue: `holds a key in plaintext; write api_key = "keyring:<path>"`})
	resp := c.call(4, "config.reload", nil)
	if resp.Error == nil || resp.Error.Code != codeConfigInvalid {
		t.Fatalf("a refused reload = %+v, want CONFIG_INVALID", resp.Error)
	}
	raw, _ := json.Marshal(resp.Error.Data)
	if !strings.Contains(string(raw), "providers.leaky.api_key") || !strings.Contains(string(raw), "keyring:") {
		t.Errorf("CONFIG_INVALID data = %s, want the rejected entry in details", raw)
	}

	cli := dial(t, s)
	if resp := cli.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := cli.call(2, "config.get", nil); resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("config.get from umb = %+v, want METHOD_NOT_FOUND: §2 gives cli no config.*", resp.Error)
	}
}

// TestStatusCarriesEachProvidersReason_REQ_SEC_008: `umb status` can only show why a
// provider is down if system.status says so.
func TestStatusCarriesEachProvidersReason_REQ_SEC_008(t *testing.T) {
	t.Parallel()

	s := testServer(t, func(context.Context) (StatusResult, error) {
		return StatusResult{Providers: []ProviderStatus{
			{ID: "hf", Health: "down", Reason: "keyring_unavailable"},
			{ID: "openrouter", Health: "degraded", Reason: "env_secret"},
			{ID: "ollama", Health: "unknown"},
		}}, nil
	})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	resp := c.call(2, "system.status", nil)
	raw, _ := json.Marshal(resp.Result)
	for _, want := range []string{`"reason":"keyring_unavailable"`, `"reason":"env_secret"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("system.status = %s, want %s", raw, want)
		}
	}
	if strings.Contains(string(raw), `"id":"ollama","health":"unknown","reason"`) {
		t.Errorf("a provider with nothing wrong carries an empty reason: %s", raw)
	}
}
