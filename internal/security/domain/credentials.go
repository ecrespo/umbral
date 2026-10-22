package domain

import (
	"errors"
	"fmt"
)

// Errors a keyring lookup reports, as resolution understands them.
var (
	// ErrKeyringUnavailable is a keyring the machine does not have or cannot reach:
	// headless Linux without Secret Service, a locked keychain (REQ-SEC-008).
	ErrKeyringUnavailable = errors.New("keyring unavailable")
	// ErrSecretNotFound is a reachable store that holds nothing under the name.
	ErrSecretNotFound = errors.New("secret not found")
)

// Source is where a provider's credential comes from.
type Source string

// Credential sources, as a models file names them.
const (
	SourceNone    Source = "none"
	SourceKeyring Source = "keyring"
	SourceEnv     Source = "env"
)

// Health is a provider's state as `system.status` reports it (API Spec §5.2).
type Health string

// Provider health values. `unknown` is a provider with what it needs to start and nothing
// yet known about whether it answers; the gateway replaces it once it has asked.
const (
	HealthUnknown  Health = "unknown"
	HealthDegraded Health = "degraded"
	HealthDown     Health = "down"
)

// Reasons a provider is down or degraded, shown by `umb status`.
const (
	// ReasonKeyringUnavailable: its key is in a keyring this machine cannot reach (REQ-SEC-008).
	ReasonKeyringUnavailable = "keyring_unavailable"
	// ReasonEnvSecret: its key came from the environment, the weaker store (REQ-SEC-012).
	ReasonEnvSecret = "env_secret"
	// ReasonEnvNotAllowed: it names `env:<VAR>`, which is only accepted where the keyring
	// is unavailable and `[secrets] allow_env = true` (REQ-SEC-012).
	ReasonEnvNotAllowed = "env_secret_not_allowed"
	// ReasonSecretNotFound: the store it names holds nothing under that name.
	ReasonSecretNotFound = "secret_not_found"
	// ReasonKeyringError: the keyring answered the probe and then failed this lookup.
	ReasonKeyringError = "keyring_error"
	// ReasonPlaintextSecret: its entry was refused for a key in the clear (REQ-SEC-004).
	ReasonPlaintextSecret = "plaintext_secret"
)

// Secret holds a credential value. It formats as a placeholder under every verb, so a
// secret that reaches a log line or an error message by accident reaches it as
// "[REDACTED]"; Reveal is the one way to the value, and its callers are the few that send
// it to a provider.
type Secret struct{ value string }

// NewSecret wraps a value.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the value.
func (s Secret) Reveal() string { return s.value }

// IsZero reports whether there is no value.
func (s Secret) IsZero() bool { return s.value == "" }

const redacted = "[REDACTED]"

// String keeps %v, %s and %q from printing the value.
func (s Secret) String() string { return redacted }

// GoString keeps %#v from printing it.
func (s Secret) GoString() string { return redacted }

// Format covers the verbs String and GoString do not reach, such as %+v and %x.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// CredentialRequest is what resolution needs to know about one provider.
type CredentialRequest struct {
	ProviderID string
	Source     Source
	// Ref is the keyring path or the environment variable name; empty for SourceNone.
	Ref string
}

// ResolvedCredential is one provider's outcome.
type ResolvedCredential struct {
	ProviderID string
	Health     Health
	// Reason says why Health is down or degraded; empty otherwise.
	Reason string
	// Secret is zero whenever the provider may not start with a credential.
	Secret Secret
}

// Lookups are the two stores resolution reads, injected so the rules stay pure.
type Lookups struct {
	// KeyringAvailable is the result of probing the keyring once, at start or on reload.
	KeyringAvailable bool
	// Keyring reads one path; it returns ErrSecretNotFound when the store holds nothing.
	Keyring func(path string) (string, error)
	// Env reads one variable.
	Env func(name string) (string, bool)
	// AllowEnv is `[secrets] allow_env`.
	AllowEnv bool
}

// ResolveCredentials applies REQ-SEC-004, REQ-SEC-008 and REQ-SEC-012 to every provider and
// reports each one, so a disabled provider is visible rather than absent.
//
//   - No credential: the provider needs none (a local Ollama) and is `unknown`.
//   - `keyring:<path>`: read from the keyring. With the keyring unavailable the provider is
//     down with `keyring_unavailable`, and the daemon carries on without it (REQ-SEC-008).
//   - `env:<VAR>`: accepted only where the keyring is unavailable and allow_env is on, and
//     then the provider is `degraded` with `env_secret` (REQ-SEC-012). Anywhere else it is
//     down with `env_secret_not_allowed`: with a working keyring, the keyring is the store.
func ResolveCredentials(requests []CredentialRequest, l Lookups) []ResolvedCredential {
	out := make([]ResolvedCredential, 0, len(requests))
	for _, req := range requests {
		out = append(out, resolve(req, l))
	}
	return out
}

func resolve(req CredentialRequest, l Lookups) ResolvedCredential {
	down := func(reason string) ResolvedCredential {
		return ResolvedCredential{ProviderID: req.ProviderID, Health: HealthDown, Reason: reason}
	}

	switch req.Source {
	case SourceKeyring:
		if !l.KeyringAvailable {
			return down(ReasonKeyringUnavailable)
		}
		value, err := l.Keyring(req.Ref)
		switch {
		case errors.Is(err, ErrSecretNotFound) || (err == nil && value == ""):
			return down(ReasonSecretNotFound)
		case errors.Is(err, ErrKeyringUnavailable):
			return down(ReasonKeyringUnavailable)
		case err != nil:
			return down(ReasonKeyringError)
		}
		return ResolvedCredential{ProviderID: req.ProviderID, Health: HealthUnknown, Secret: NewSecret(value)}

	case SourceEnv:
		if l.KeyringAvailable || !l.AllowEnv {
			return down(ReasonEnvNotAllowed)
		}
		value, ok := l.Env(req.Ref)
		if !ok || value == "" {
			return down(ReasonSecretNotFound)
		}
		return ResolvedCredential{
			ProviderID: req.ProviderID, Health: HealthDegraded, Reason: ReasonEnvSecret,
			Secret: NewSecret(value),
		}

	default:
		return ResolvedCredential{ProviderID: req.ProviderID, Health: HealthUnknown}
	}
}
