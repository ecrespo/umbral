package keyring

import (
	"errors"
	"testing"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/ecrespo/umbral/internal/security/domain"
)

// The go-keyring mock is process-wide, so these tests are not parallel.

// TestPathsSplitIntoServiceAndAccount: `keyring:umbral/openrouter` is service `umbral`,
// account `openrouter`, which is how every OS keyring UI shows it; a path with no slash
// lives under the `umbral` service.
func TestPathsSplitIntoServiceAndAccount(t *testing.T) {
	gokeyring.MockInit()
	if err := gokeyring.Set("umbral", "openrouter", "sk-1"); err != nil {
		t.Fatal(err)
	}
	if err := gokeyring.Set("work", "gitlab/token", "glpat-2"); err != nil {
		t.Fatal(err)
	}
	k := New()

	for path, want := range map[string]string{
		"umbral/openrouter": "sk-1",
		"openrouter":        "sk-1",
		"work/gitlab/token": "glpat-2",
	} {
		got, err := k.Get(t.Context(), path)
		if err != nil || got != want {
			t.Errorf("Get(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := k.Get(t.Context(), "umbral/absent"); !errors.Is(err, domain.ErrSecretNotFound) {
		t.Errorf("an absent secret = %v, want ErrSecretNotFound", err)
	}
	if err := k.Probe(t.Context()); err != nil {
		t.Errorf("Probe on a working keyring = %v", err)
	}
}

// TestAnUnreachableKeyringIsUnavailable_REQ_SEC_008: whatever the platform's store says when
// it cannot be reached, the daemon hears ErrKeyringUnavailable, from the probe and from a
// lookup alike, and carries on.
func TestAnUnreachableKeyringIsUnavailable_REQ_SEC_008(t *testing.T) {
	gokeyring.MockInitWithError(errors.New("The name org.freedesktop.secrets was not provided by any .service files"))
	k := New()

	if err := k.Probe(t.Context()); !errors.Is(err, domain.ErrKeyringUnavailable) {
		t.Errorf("Probe = %v, want ErrKeyringUnavailable", err)
	}
	if _, err := k.Get(t.Context(), "umbral/openrouter"); !errors.Is(err, domain.ErrKeyringUnavailable) {
		t.Errorf("Get = %v, want ErrKeyringUnavailable", err)
	}
}
