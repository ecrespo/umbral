package keyring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
)

// childEnv marks the test process that talks to the real Credential Manager. The package's other
// tests replace go-keyring's provider with a process-wide mock that cannot be undone, so the
// real round trip runs in a fresh process running this test alone.
const childEnv = "UMBRAL_KEYRING_WINCRED_CHILD"

// TestSecretsAreReadFromTheCredentialManager_REQ_WIN_003: a secret stored in the Windows Credential
// Manager — where `cmdkey` lists it — is what `keyring:<service>/<account>` resolves to.
func TestSecretsAreReadFromTheCredentialManager_REQ_WIN_003(t *testing.T) {
	if os.Getenv(childEnv) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSecretsAreReadFromTheCredentialManager_REQ_WIN_003$", "-test.v")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS") {
			t.Fatalf("the Credential Manager round trip failed (%v):\n%s", err, out)
		}
		return
	}

	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	service := "umbral-test-" + hex.EncodeToString(raw)
	if err := gokeyring.Set(service, "openrouter", "sk-windows"); err != nil {
		t.Fatalf("store a credential: %v", err)
	}
	t.Cleanup(func() { _ = gokeyring.Delete(service, "openrouter") })

	out, err := exec.CommandContext(t.Context(), "cmdkey", "/list:"+service+":openrouter").CombinedOutput()
	if err != nil || !strings.Contains(string(out), service) {
		t.Fatalf("cmdkey does not list the credential (%v): %s", err, out)
	}

	k := New()
	if err := k.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	got, err := k.Get(t.Context(), service+"/openrouter")
	if err != nil || got != "sk-windows" {
		t.Fatalf("Get = %q, %v; want the stored secret", got, err)
	}
}
