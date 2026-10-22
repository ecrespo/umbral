// Package keyring implements ports.Keyring over the operating system's keyring with
// zalando/go-keyring: Secret Service on Linux, Keychain on macOS.
package keyring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
)

// defaultService is the service a path without a slash lives under.
const defaultService = "umbral"

// probeAccount is looked up to learn whether the keyring answers at all. Its absence is the
// expected answer: a keyring that says "not found" is a keyring that works.
const probeAccount = "__umbral_probe__"

// lookupTimeout bounds one call. A Secret Service that is registered but wedged can block a
// D-Bus call indefinitely, and the daemon must start without it (REQ-SEC-008).
const lookupTimeout = 3 * time.Second

// Keyring is the OS keyring.
type Keyring struct{}

var _ ports.Keyring = Keyring{}

// New returns the OS keyring.
func New() Keyring { return Keyring{} }

// Probe implements ports.Keyring.
func (k Keyring) Probe(ctx context.Context) error {
	_, err := k.get(ctx, defaultService, probeAccount)
	if errors.Is(err, domain.ErrSecretNotFound) {
		return nil
	}
	return err
}

// Get implements ports.Keyring.
func (k Keyring) Get(ctx context.Context, path string) (string, error) {
	service, account := split(path)
	return k.get(ctx, service, account)
}

// split reads `service/account`; a path with no slash is an account of the umbral service.
func split(path string) (service, account string) {
	if s, a, found := strings.Cut(path, "/"); found && s != "" && a != "" {
		return s, a
	}
	return defaultService, path
}

func (Keyring) get(ctx context.Context, service, account string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	type answer struct {
		value string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		v, err := gokeyring.Get(service, account)
		done <- answer{v, err}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("%w: no answer within %v", domain.ErrKeyringUnavailable, lookupTimeout)
	case a := <-done:
		switch {
		case a.err == nil:
			return a.value, nil
		case errors.Is(a.err, gokeyring.ErrNotFound):
			return "", domain.ErrSecretNotFound
		default:
			// Every other failure — no Secret Service, no D-Bus session, a locked
			// keychain — means the same thing to the daemon. The platform's text is kept
			// for the log; it names a service, never a secret.
			return "", fmt.Errorf("%w: %w", domain.ErrKeyringUnavailable, a.err)
		}
	}
}
