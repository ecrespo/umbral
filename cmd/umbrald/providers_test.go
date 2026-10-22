package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// lockedBuffer is a log destination the daemon's process writer and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	envSecretValue       = "sk-or-v1-e2e-environment-secret-value"
	plaintextSecretValue = "sk-live-plaintext-in-the-models-file"
)

const e2eModels = `
[[providers]]
id = "ollama"
type = "ollama"
base_url = "http://127.0.0.1:9"

[[providers]]
id = "hf"
type = "openai-compat"
base_url = "https://router.huggingface.co/v1"
api_key = "keyring:umbral/hf_token"

# The daemon discovers the models of every provider it may run (REQ-LLM-002), so a test
# never names a real endpoint: port 9 on loopback refuses at once.
[[providers]]
id = "openrouter"
type = "openrouter"
base_url = "http://127.0.0.1:9/api/v1"
api_key = "env:UMBRAL_E2E_OPENROUTER_KEY"

[[providers]]
id = "leaky"
type = "openai-compat"
base_url = "https://example.com/v1"
api_key = "` + plaintextSecretValue + `"
`

// TestADaemonWithoutAKeyringStartsDegraded_REQ_SEC_008 runs the three secret requirements
// against a real daemon on a machine whose keyring cannot be reached: D-Bus points at a
// socket that does not exist, which is a headless Linux without Secret Service.
//
//   - REQ-SEC-008: the daemon starts; the keyring provider is down with keyring_unavailable.
//   - REQ-SEC-012: with allow_env on, the env: provider is degraded with env_secret; after a
//     reload with allow_env off, it is down, and nothing runs with a credential.
//   - REQ-SEC-004: the plaintext entry is refused and reported, and a reload that would still
//     carry it is CONFIG_INVALID and changes nothing.
//
// Neither secret appears anywhere in what the daemon logged.
func TestADaemonWithoutAKeyringStartsDegraded_REQ_SEC_008(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the keyring is made unreachable through D-Bus, which only Linux uses")
	}
	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(configDir, "config.toml")
	models := filepath.Join(configDir, "models.toml")
	writeFile(t, settings, "[secrets]\nallow_env = true\n")
	writeFile(t, models, e2eModels)

	logs := &lockedBuffer{}
	startDaemonLoggingTo(t, bin, rt, logs,
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(t.TempDir(), "no-bus"),
		"UMBRAL_E2E_OPENROUTER_KEY="+envSecretValue)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatalf("the daemon did not start without a keyring: %v", err)
	}
	defer func() { _ = c.Close() }()

	type provider struct{ ID, Health, Reason string }
	statuses := func() map[string]provider {
		var status struct {
			Providers []provider `json:"providers"`
		}
		if err := c.Call(ctx, "system.status", nil, &status); err != nil {
			t.Fatalf("system.status: %v", err)
		}
		out := map[string]provider{}
		for _, p := range status.Providers {
			out[p.ID] = p
		}
		return out
	}

	got := statuses()
	for id, want := range map[string]provider{
		"ollama":     {"ollama", "unknown", ""},
		"hf":         {"hf", "down", "keyring_unavailable"},
		"openrouter": {"openrouter", "degraded", "env_secret"},
		"leaky":      {"leaky", "down", "plaintext_secret"},
	} {
		if got[id] != want {
			t.Errorf("%s = %+v, want %+v", id, got[id], want)
		}
	}

	var view struct {
		Providers []struct{ ID, Credential string }
		Rejected  []struct {
			ProviderID string `json:"provider_id"`
			Issue      string
		}
	}
	if err := c.Call(ctx, "config.get", nil, &view); err != nil {
		t.Fatalf("config.get: %v", err)
	}
	if len(view.Rejected) != 1 || view.Rejected[0].ProviderID != "leaky" ||
		!strings.Contains(view.Rejected[0].Issue, "keyring:<path>") {
		t.Errorf("rejected = %+v, want the leaky entry naming keyring:<path>", view.Rejected)
	}
	for _, p := range view.Providers {
		if p.ID == "openrouter" && p.Credential != "env:UMBRAL_E2E_OPENROUTER_KEY" {
			t.Errorf("openrouter's credential = %q, want the reference", p.Credential)
		}
	}

	// A reload that still carries the plaintext entry is refused, and nothing changes.
	err = c.Call(ctx, "config.reload", nil, nil)
	if client.DomainCode(err) != "CONFIG_INVALID" || !strings.Contains(err.Error(), "providers.leaky.api_key") {
		t.Errorf("reload with a plaintext key = %v, want CONFIG_INVALID naming providers.leaky.api_key", err)
	}
	if statuses()["openrouter"].Health != "degraded" {
		t.Error("a refused reload changed the providers")
	}

	// Without the plaintext entry and with allow_env off, the reload applies: the env
	// provider is down, and no provider runs with a credential.
	writeFile(t, models, strings.Split(e2eModels, "[[providers]]\nid = \"leaky\"")[0])
	writeFile(t, settings, "[secrets]\nallow_env = false\n")
	if err := c.Call(ctx, "config.reload", nil, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got = statuses()
	if or := got["openrouter"]; or.Health != "down" || or.Reason != "env_secret_not_allowed" {
		t.Errorf("openrouter with allow_env off = %+v, want down / env_secret_not_allowed", or)
	}
	if _, still := got["leaky"]; still {
		t.Error("the removed entry is still reported")
	}

	text := logs.String()
	for _, secret := range []string{envSecretValue, plaintextSecretValue} {
		if strings.Contains(text, secret) {
			t.Errorf("the daemon's log carries a secret (%s…)", secret[:8])
		}
	}
	if !strings.Contains(text, `"reason":"keyring_unavailable"`) {
		t.Errorf("the log does not say why hf is down:\n%s", text)
	}
}
