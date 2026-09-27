package domain

import (
	"errors"
	"fmt"
	"testing"
)

// fakeLookups builds the two lookups resolution uses: a keyring holding the given paths, and
// an environment holding the given variables. The keyring answers even when keyringUp is
// false, so a test proves the probe's verdict is obeyed rather than rediscovered.
func fakeLookups(keyringUp bool, keyring, env map[string]string, allowEnv bool) Lookups {
	return Lookups{
		KeyringAvailable: keyringUp,
		Keyring: func(path string) (string, error) {
			v, ok := keyring[path]
			if !ok {
				return "", ErrSecretNotFound
			}
			return v, nil
		},
		Env: func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		},
		AllowEnv: allowEnv,
	}
}

var requests = []CredentialRequest{
	{ProviderID: "ollama", Source: SourceNone},
	{ProviderID: "hf", Source: SourceKeyring, Ref: "umbral/hf_token"},
	{ProviderID: "openrouter", Source: SourceEnv, Ref: "OPENROUTER_API_KEY"},
}

func byID(resolved []ResolvedCredential) map[string]ResolvedCredential {
	out := map[string]ResolvedCredential{}
	for _, r := range resolved {
		out[r.ProviderID] = r
	}
	return out
}

// TestKeyringUnavailableDisablesProviders_REQ_SEC_008: with no keyring, the daemon still
// starts; every provider whose key lives in the keyring is down with reason
// `keyring_unavailable` and has no credential, and a provider that needs none is untouched.
func TestKeyringUnavailableDisablesProviders_REQ_SEC_008(t *testing.T) {
	t.Parallel()

	got := byID(ResolveCredentials(requests,
		fakeLookups(false, map[string]string{"umbral/hf_token": "hf_would_be_read"}, nil, false)))

	hf := got["hf"]
	if hf.Health != HealthDown || hf.Reason != ReasonKeyringUnavailable || !hf.Secret.IsZero() {
		t.Errorf("hf = %+v, want down / keyring_unavailable with no secret", hf)
	}
	if o := got["ollama"]; o.Health != HealthUnknown || o.Reason != "" {
		t.Errorf("ollama = %+v, want unknown with no reason: it needs no key", o)
	}
	if len(got) != len(requests) {
		t.Errorf("resolved %d providers, want every one of %d reported", len(got), len(requests))
	}
}

// TestEnvFallbackOnlyWhenEnabled_REQ_SEC_012: `env:<VAR>` stands in for the keyring only
// where the keyring is unavailable *and* `[secrets] allow_env = true`; then the provider is
// degraded with reason `env_secret`. With `allow_env = false` and no keyring, no provider
// starts with a credential — the task's Done line.
func TestEnvFallbackOnlyWhenEnabled_REQ_SEC_012(t *testing.T) {
	t.Parallel()

	env := map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-secret"}
	keyring := map[string]string{"umbral/hf_token": "hf_secret"}

	for _, tc := range []struct {
		name      string
		keyringUp bool
		allowEnv  bool
		health    Health
		reason    string
		secret    string
	}{
		{"no keyring, allow_env on: degraded", false, true, HealthDegraded, ReasonEnvSecret, "sk-or-v1-secret"},
		{"no keyring, allow_env off: down", false, false, HealthDown, ReasonEnvNotAllowed, ""},
		{"keyring up, allow_env on: the keyring is the store", true, true, HealthDown, ReasonEnvNotAllowed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := byID(ResolveCredentials(requests, fakeLookups(tc.keyringUp, keyring, env, tc.allowEnv)))
			or := got["openrouter"]
			if or.Health != tc.health || or.Reason != tc.reason || or.Secret.Reveal() != tc.secret {
				t.Errorf("openrouter = %s/%s with secret %q, want %s/%s with %q",
					or.Health, or.Reason, or.Secret.Reveal(), tc.health, tc.reason, tc.secret)
			}
		})
	}

	t.Run("allow_env off and no keyring: nothing starts with a credential", func(t *testing.T) {
		t.Parallel()
		for _, r := range ResolveCredentials(requests, fakeLookups(false, keyring, env, false)) {
			if !r.Secret.IsZero() {
				t.Errorf("%s started with a credential", r.ProviderID)
			}
		}
	})

	t.Run("a variable that is unset or empty is a missing secret", func(t *testing.T) {
		t.Parallel()
		for _, env := range []map[string]string{{}, {"OPENROUTER_API_KEY": ""}} {
			or := byID(ResolveCredentials(requests, fakeLookups(false, nil, env, true)))["openrouter"]
			if or.Health != HealthDown || or.Reason != ReasonSecretNotFound {
				t.Errorf("env %v: openrouter = %+v, want down / secret_not_found", env, or)
			}
		}
	})
}

// TestKeyringResolution covers the keyring's own outcomes: a key found, a key missing, and a
// keyring that fails on one lookup after answering the probe.
func TestKeyringResolution(t *testing.T) {
	t.Parallel()

	got := byID(ResolveCredentials(requests, fakeLookups(true, map[string]string{"umbral/hf_token": "hf_x"}, nil, false)))
	if hf := got["hf"]; hf.Health != HealthUnknown || hf.Reason != "" || hf.Secret.Reveal() != "hf_x" {
		t.Errorf("hf with its key = %+v", hf)
	}

	got = byID(ResolveCredentials(requests, fakeLookups(true, nil, nil, false)))
	if hf := got["hf"]; hf.Health != HealthDown || hf.Reason != ReasonSecretNotFound {
		t.Errorf("hf without its key = %+v, want down / secret_not_found", hf)
	}

	gone := fakeLookups(true, nil, nil, false)
	gone.Keyring = func(string) (string, error) { return "", ErrKeyringUnavailable }
	if hf := byID(ResolveCredentials(requests, gone))["hf"]; hf.Health != HealthDown || hf.Reason != ReasonKeyringUnavailable {
		t.Errorf("hf on a keyring lost after the probe = %+v, want down / keyring_unavailable", hf)
	}

	failing := fakeLookups(true, nil, nil, false)
	failing.Keyring = func(string) (string, error) { return "", errors.New("dbus: connection reset") }
	if hf := byID(ResolveCredentials(requests, failing))["hf"]; hf.Health != HealthDown || hf.Reason != ReasonKeyringError {
		t.Errorf("hf on a failing keyring = %+v, want down / keyring_error", hf)
	}
}

// TestASecretNeverPrints: a resolved credential passes through logs and error messages by
// accident sooner or later, so every way Go formats a value gives the placeholder
// (REQ-SEC-012: "never write the value to logs").
func TestASecretNeverPrints(t *testing.T) {
	t.Parallel()

	s := NewSecret("sk-live-abcdef")
	r := ResolvedCredential{ProviderID: "p", Secret: s}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		for _, value := range []any{s, r, &r} {
			if out := fmt.Sprintf(format, value); contains(out, "sk-live") {
				t.Errorf("fmt %s of %T printed the secret: %s", format, value, out)
			}
		}
	}
	if s.Reveal() != "sk-live-abcdef" {
		t.Error("Reveal lost the value")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
