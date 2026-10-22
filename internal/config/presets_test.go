package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPresetsLoad_REQ_LLM_007: the shipped example, examples/models.toml, loads as it is with
// nothing refused, and carries the Hugging Face router and OmniRoute presets as openai-compat
// providers at their documented URLs, with keys only as keyring references. Every class
// candidate names a provider the file defines.
func TestPresetsLoad_REQ_LLM_007(t *testing.T) {
	t.Parallel()

	m, err := LoadModels(filepath.Join("..", "..", "examples", "models.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Rejected) != 0 {
		t.Fatalf("the example refuses entries: %+v", m.Rejected)
	}
	byID := map[string]Provider{}
	for _, p := range m.Providers {
		byID[p.ID] = p
		if p.Credential.Kind != CredentialNone && p.Credential.Kind != CredentialKeyring {
			t.Errorf("provider %s takes its key from %s; the example must use the keyring", p.ID, p.Credential.Kind)
		}
	}
	if len(Presets) != 2 {
		t.Fatalf("presets = %d, want the Hugging Face router and OmniRoute", len(Presets))
	}
	for _, preset := range Presets {
		got, ok := byID[preset.Provider.ID]
		if !ok {
			t.Errorf("preset %s is not in the example", preset.Name)
			continue
		}
		if preset.Provider.Type != "openai-compat" || got.Type != "openai-compat" {
			t.Errorf("preset %s: type %q in code, %q in the example; want openai-compat", preset.Name, preset.Provider.Type, got.Type)
		}
		if got.BaseURL != preset.Provider.BaseURL {
			t.Errorf("preset %s: base_url %q in the example, %q in code", preset.Name, got.BaseURL, preset.Provider.BaseURL)
		}
		if got.Credential != preset.Provider.Credential {
			t.Errorf("preset %s: credential %v in the example, %v in code", preset.Name, got.Credential, preset.Provider.Credential)
		}
	}
	if hf := byID["hf"]; hf.BaseURL != "https://router.huggingface.co/v1" || hf.Credential.String() != "keyring:umbral/huggingface" {
		t.Errorf("hf = %+v, want REQ-LLM-007's URL and a keyring key", hf)
	}
	for class, candidates := range m.Classes {
		for _, c := range candidates {
			provider, _, _ := strings.Cut(c, "/")
			if _, ok := byID[provider]; !ok {
				t.Errorf("class %s names %s, whose provider the example does not define", class, c)
			}
		}
	}
}
