package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// ollamaEntry is an OpenCode entry for a local Ollama, as the live tests write it.
func ollamaEntry(baseURL string) map[string]any {
	return map[string]any{
		"model": "ollama/gpt-oss:20b",
		"provider": map[string]any{
			"ollama": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"options": map[string]any{"baseURL": baseURL},
				"models":  map[string]any{"gpt-oss:20b": map[string]any{"name": "gpt-oss:20b"}},
			},
		},
	}
}

func baseURLOf(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	opts, ok := cfg["provider"].(map[string]any)["ollama"].(map[string]any)["options"].(map[string]any)
	if !ok {
		t.Fatalf("no options in %v", cfg)
	}
	return opts
}

// TestOpenCodeIsPointedAtTheRelay_REQ_ACP_009: behind a relay the agent's provider is reached at
// the relay's address, on the path its entry names, with the relay's token as its key; the
// entry the daemon read is left as it was.
func TestOpenCodeIsPointedAtTheRelay_REQ_ACP_009(t *testing.T) {
	m := Builtin()[ManifestOpenCode]
	if m.PortFlag != "--port" || m.Relay == nil {
		t.Fatalf("the OpenCode manifest cannot be given a relay: port flag %q", m.PortFlag)
	}
	entry := ollamaEntry("http://127.0.0.1:11434/v1")
	out, err := m.Relay(entry, "http://127.0.0.1:11434", "http://127.0.0.1:40001", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := baseURLOf(t, out), map[string]any{"baseURL": "http://127.0.0.1:40001/v1", "apiKey": "tok"}; !reflect.DeepEqual(got, want) {
		t.Errorf("options = %v, want %v", got, want)
	}
	if got := baseURLOf(t, entry); !reflect.DeepEqual(got, map[string]any{"baseURL": "http://127.0.0.1:11434/v1"}) {
		t.Errorf("the entry itself was changed: %v", got)
	}
	if out["model"] != "ollama/gpt-oss:20b" {
		t.Errorf("the model was not kept: %v", out)
	}
}

// TestARelayEntryMustNameItsProvidersEndpoint_REQ_ACP_009: the agent's endpoint must be the
// models.toml provider's, or below it; anything else is refused, since the relay would forward
// it to an endpoint the entry did not name.
func TestARelayEntryMustNameItsProvidersEndpoint_REQ_ACP_009(t *testing.T) {
	m := Builtin()[ManifestOpenCode]
	const upstream = "http://127.0.0.1:11434/v1"
	if _, err := m.Relay(ollamaEntry("http://127.0.0.1:11434/v1/"), upstream, "http://127.0.0.1:1", "t"); err != nil {
		t.Errorf("the provider's own endpoint was refused: %v", err)
	}
	for name, cfg := range map[string]map[string]any{
		"another host":   ollamaEntry("http://10.0.0.2:11434/v1"),
		"another port":   ollamaEntry("http://127.0.0.1:11435/v1"),
		"another scheme": ollamaEntry("https://127.0.0.1:11434/v1"),
		"above it":       ollamaEntry("http://127.0.0.1:11434/"),
		"beside it":      ollamaEntry("http://127.0.0.1:11434/v10"),
		"a query":        ollamaEntry("http://127.0.0.1:11434/v1?x=1"),
		"a user":         ollamaEntry("http://u@127.0.0.1:11434/v1"),
		"a fragment":     ollamaEntry("http://127.0.0.1:11434/v1#x"),
		"no base URL":    {"model": "ollama/m", "provider": map[string]any{"ollama": map[string]any{}}},
		"no provider":    {"model": "ollama/m"},
		"no model":       {"provider": ollamaEntry(upstream)["provider"]},
		"a bare model":   {"model": "m", "provider": ollamaEntry(upstream)["provider"]},
	} {
		_, err := m.Relay(cfg, upstream, "http://127.0.0.1:1", "t")
		var r *Refusal
		if !errors.As(err, &r) || r.Reason != ReasonRelay {
			t.Errorf("%s: %v, want a %q refusal", name, err, ReasonRelay)
		}
	}
}

// TestARelayEntryTakesNoEnvRefs_REQ_ACP_009: behind a relay the agent holds no secret of the
// user's, so an entry that names both is refused at start rather than one being dropped.
func TestARelayEntryTakesNoEnvRefs_REQ_ACP_009(t *testing.T) {
	e := Entry{
		Name: "oc", Manifest: ManifestOpenCode, Command: "opencode", Relay: "ollama",
		EnvRefs: map[string]string{"OLLAMA_KEY": "keyring:umbral/ollama"},
	}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "env_refs") {
		t.Errorf("Validate = %v, want env_refs refused beside relay", err)
	}
	e.EnvRefs = nil
	if err := e.Validate(); err != nil {
		t.Errorf("a relay entry without env_refs: %v", err)
	}
	e.Relay = "Not A Provider!"
	if err := e.Validate(); err == nil {
		t.Error("a relay that names no provider id was accepted")
	}
}
