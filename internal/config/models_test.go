package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModels(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ModelsFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// exampleModels is docs/ARCHITECTURE.md §7's file, trimmed to what F1 serves.
const exampleModels = `
[router]
policy = "local-first"
offline = false
max_cost_usd_per_thread = 1.5

[classes]
fast = ["ollama/qwen3:4b"]
code = ["ollama/gpt-oss:20b", "openrouter/moonshotai/kimi-k2"]
plan = ["hf/openai/gpt-oss-120b:cerebras"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = "http://127.0.0.1:11434"
[providers.options]
num_ctx = 32768
keep_alive = "30m"

[[providers]]
id = "hf"
type = "openai-compat"
base_url = "https://router.huggingface.co/v1"
api_key = "keyring:umbral/hf_token"

[[providers]]
id = "openrouter"
type = "openrouter"
base_url = "https://openrouter.ai/api/v1"
api_key = "env:OPENROUTER_API_KEY"
`

// TestModelsFileLoads reads the documented example into typed configuration: the router,
// the classes in order, and each provider with its credential parsed into a reference.
func TestModelsFileLoads(t *testing.T) {
	t.Parallel()

	models, err := LoadModels(writeModels(t, exampleModels))
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if models.Router.Policy != "local-first" || models.Router.Offline ||
		models.Router.MaxCostMicroUSDPerThread != 1_500_000 {
		t.Errorf("router = %+v", models.Router)
	}
	if got := models.Classes["code"]; len(got) != 2 || got[0] != "ollama/gpt-oss:20b" {
		t.Errorf("classes.code = %v", got)
	}
	if len(models.Providers) != 3 || len(models.Rejected) != 0 {
		t.Fatalf("providers = %+v, rejected = %+v", models.Providers, models.Rejected)
	}
	byID := map[string]Provider{}
	for _, p := range models.Providers {
		byID[p.ID] = p
	}
	if c := byID["ollama"].Credential; c.Kind != CredentialNone {
		t.Errorf("ollama credential = %+v, want none", c)
	}
	if c := byID["hf"].Credential; c.Kind != CredentialKeyring || c.Ref != "umbral/hf_token" {
		t.Errorf("hf credential = %+v", c)
	}
	if c := byID["openrouter"].Credential; c.Kind != CredentialEnv || c.Ref != "OPENROUTER_API_KEY" {
		t.Errorf("openrouter credential = %+v", c)
	}
	if byID["ollama"].Options["num_ctx"] != int64(32768) {
		t.Errorf("ollama options = %v", byID["ollama"].Options)
	}
}

// TestAnAbsentModelsFileIsNoProviders: Umbral runs with no configuration; the agent simply
// has no model to call until one is configured.
func TestAnAbsentModelsFileIsNoProviders(t *testing.T) {
	t.Parallel()

	models, err := LoadModels(filepath.Join(t.TempDir(), ModelsFileName))
	if err != nil || len(models.Providers) != 0 {
		t.Fatalf("absent file = %+v, %v; want no providers and no error", models, err)
	}
}

// TestPlaintextKeyRejected_REQ_SEC_004: a key written in the clear is refused with its
// entry, the refusal says to use keyring:<path>, and the value appears nowhere in it. The
// other providers load: the entry is rejected, not the file.
func TestPlaintextKeyRejected_REQ_SEC_004(t *testing.T) {
	t.Parallel()

	// Low entropy on purpose: a realistic key here is a finding for gitleaks (Art. 1), and
	// the parser needs only something that is not a reference.
	const secret = "PLAINTEXT-TEST-VALUE-NOT-A-REFERENCE"
	body := exampleModels + `
[[providers]]
id = "leaky"
type = "openai-compat"
base_url = "https://example.com/v1"
api_key = "` + secret + `"
`
	models, err := LoadModels(writeModels(t, body))
	if err != nil {
		t.Fatalf("a plaintext key rejected its entry and failed the whole file: %v", err)
	}
	if len(models.Rejected) != 1 || models.Rejected[0].ProviderID != "leaky" {
		t.Fatalf("rejected = %+v, want the leaky entry", models.Rejected)
	}
	for _, p := range models.Providers {
		if p.ID == "leaky" {
			t.Error("the rejected entry is still among the providers")
		}
	}
	r := models.Rejected[0]
	if r.Field != "api_key" || !strings.Contains(r.Issue, "keyring:<path>") {
		t.Errorf("rejection = %+v, want it to name api_key and keyring:<path>", r)
	}
	if strings.Contains(r.Issue, secret) {
		t.Error("the rejection repeats the secret it refused")
	}
}

// TestASecretInOptionsIsRejected_REQ_SEC_004: api_key is not the only place a key can be
// pasted; an option named like a credential rejects its entry the same way, and keep_alive,
// which contains no such word, does not.
func TestASecretInOptionsIsRejected_REQ_SEC_004(t *testing.T) {
	t.Parallel()

	const value = "PLAINTEXT-OPTION-VALUE"
	for _, name := range []string{"api_key", "Authorization", "access_token", "client_secret"} {
		body := exampleModels + "\n[[providers]]\nid = \"leaky\"\ntype = \"openai-compat\"\n" +
			"base_url = \"https://example.com/v1\"\n[providers.options]\n" + name + " = \"" + value + "\"\n"
		models, err := LoadModels(writeModels(t, body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(models.Rejected) != 1 || models.Rejected[0].Field != "options."+name {
			t.Errorf("%s: rejected = %+v, want the leaky entry's options.%s", name, models.Rejected, name)
			continue
		}
		if strings.Contains(models.Rejected[0].Issue, value) {
			t.Errorf("%s: the rejection repeats the value", name)
		}
	}
}

// TestMalformedModelsFileIsInvalid: what the schema refuses stops the load, naming why, like
// every other setting (Tech Design §5.1). A typo in a key is not silently ignored.
func TestMalformedModelsFileIsInvalid(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"not TOML":              "[[providers]\nid = 1\n",
		"a provider with no id": "[[providers]]\ntype = \"ollama\"\nbase_url = \"http://x\"\n",
		"an unknown type":       "[[providers]]\nid = \"x\"\ntype = \"telepathy\"\nbase_url = \"http://x\"\n",
		"a misspelt key":        "[[providers]]\nid = \"x\"\ntype = \"ollama\"\nbase_url = \"http://x\"\napi_kee = \"keyring:a/b\"\n",
		"an unknown policy":     "[router]\npolicy = \"cheapest\"\n",
		"a cost cap past 1e6":   "[router]\nmax_cost_usd_per_thread = 2e6\n",
		// Rounded to micro-USD it would be 0, which means no cap: the opposite of what was asked.
		"a cost cap under a micro-USD": "[router]\nmax_cost_usd_per_thread = 0.0000004\n",
		"an unknown class":             "[classes]\nturbo = [\"ollama/x\"]\n",
		"two providers, one id": "[[providers]]\nid = \"x\"\ntype = \"ollama\"\nbase_url = \"http://a\"\n" +
			"[[providers]]\nid = \"x\"\ntype = \"ollama\"\nbase_url = \"http://b\"\n",
		"an empty keyring path": "[[providers]]\nid = \"x\"\ntype = \"openai-compat\"\nbase_url = \"http://a\"\napi_key = \"keyring:\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadModels(writeModels(t, body)); !errors.Is(err, ErrSettingsInvalid) {
				t.Errorf("%q: err = %v, want ErrSettingsInvalid", body, err)
			}
		})
	}
}

// TestAMalformedReferenceNeverEchoesItsValue: a key pasted after the prefix is still a key;
// the error names the provider and the field, never the value (REQ-SEC-004, Art. 5).
func TestAMalformedReferenceNeverEchoesItsValue(t *testing.T) {
	t.Parallel()

	const pasted = "PASTED KEY VALUE"
	for _, apiKey := range []string{"keyring:" + pasted, "env:" + pasted} {
		body := "[[providers]]\nid = \"x\"\ntype = \"openai-compat\"\nbase_url = \"http://a\"\napi_key = \"" + apiKey + "\"\n"
		_, err := LoadModels(writeModels(t, body))
		if !errors.Is(err, ErrSettingsInvalid) {
			t.Fatalf("%s: err = %v, want ErrSettingsInvalid", apiKey, err)
		}
		if strings.Contains(err.Error(), pasted) || !strings.Contains(err.Error(), "api_key") {
			t.Errorf("%s: error %q repeats the value or does not name the field", apiKey, err)
		}
	}
}

// TestAllowEnvIsReadFromTheSettingsFile: `[secrets] allow_env` is off unless the user says
// otherwise (REQ-SEC-012 is optional, and the fallback is the weaker store).
func TestAllowEnvIsReadFromTheSettingsFile(t *testing.T) {
	t.Parallel()

	settings, err := LoadSettings(nil, write(t, ""))
	if err != nil || settings.AllowEnvSecrets {
		t.Fatalf("default = %v, %v; want off", settings.AllowEnvSecrets, err)
	}
	settings, err = LoadSettings(nil, write(t, "[secrets]\nallow_env = true\n"))
	if err != nil || !settings.AllowEnvSecrets {
		t.Fatalf("allow_env = true read as %v, %v", settings.AllowEnvSecrets, err)
	}
}
