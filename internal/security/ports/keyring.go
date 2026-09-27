package ports

import "context"

// Keyring reads secrets from the operating system's keyring (Tech Design §6.2: API keys live
// there and only in memory past it). Paths are the `<path>` of `keyring:<path>`.
//
// Errors are the domain's: domain.ErrKeyringUnavailable when the machine has no reachable
// keyring, domain.ErrSecretNotFound when it holds nothing under the path.
type Keyring interface {
	// Probe reports whether the keyring can be used at all, once, before any lookup. A
	// headless Linux without Secret Service, or a locked keychain, is
	// domain.ErrKeyringUnavailable (REQ-SEC-008).
	Probe(ctx context.Context) error
	// Get reads one secret.
	Get(ctx context.Context, path string) (string, error)
}
