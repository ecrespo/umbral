package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// TestUmbRulesKeyAddNeedsTheFingerprint_REQ_SEC_014: without --confirm, or with another key's
// fingerprint, `umb rules key add` prints the key's fingerprint, exits 1 and sends nothing;
// with the right one it sends API §5.34's params.
func TestUmbRulesKeyAddNeedsTheFingerprint_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)
	fp := secdomain.Fingerprint(pub)
	seen := make(chan map[string]any, 8)
	socket := serveFakeDaemonWithParams(t, map[string]any{
		"rules.key.add": map[string]any{"key": map[string]any{"id": "key_1", "fingerprint": fp, "source": "user", "added_at": 1, "revoked_at": nil}},
	}, seen)

	for name, confirm := range map[string][]string{"no confirmation": nil, "another fingerprint": {"--confirm", "SHA256:nope"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"rules", "key", "add", b64, "--socket", socket, "--no-autostart"}, confirm...)
		if code := run(args, &stdout, &stderr); code != exitFailure {
			t.Errorf("%s: exit %d", name, code)
		}
		if !strings.Contains(stdout.String(), fp) {
			t.Errorf("%s: the fingerprint was not shown: %q", name, stdout.String())
		}
	}
	var asJSON, jsonErr bytes.Buffer
	if code := run([]string{"rules", "key", "add", b64, "--json", "--socket", socket, "--no-autostart"}, &asJSON, &jsonErr); code != exitFailure ||
		strings.TrimSpace(asJSON.String()) != `{"fingerprint":"`+fp+`"}` {
		t.Errorf("--json without --confirm: exit %d, %q", code, asJSON.String())
	}
	for len(seen) > 0 {
		if p := <-seen; p["public_key"] != nil {
			t.Fatalf("an unconfirmed add reached the daemon: %v", p)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"rules", "key", "add", b64, "--confirm", fp, "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("confirmed add: exit %d, stderr %q", code, stderr.String())
	}
	var got map[string]any
	for p := range seen {
		if p["public_key"] != nil {
			got = p
			break
		}
	}
	if got["public_key"] != b64 || got["confirm_fingerprint"] != fp {
		t.Errorf("params %v", got)
	}
	if !strings.Contains(stdout.String(), "trusted "+fp) {
		t.Errorf("stdout %q", stdout.String())
	}
}

// TestUmbRulesStatus_REQ_SEC_016: `umb rules status` prints the bundle in force, the update
// state and the trust store; `umb rules reset` sends no params.
func TestUmbRulesStatus_REQ_SEC_016(t *testing.T) {
	t.Parallel()
	status := map[string]any{
		"active_bundle":   map[string]any{"version": 7, "sha256": "a7", "source": "remote", "verified_with": "key_1"},
		"previous_bundle": map[string]any{"version": 6, "sha256": "a6"},
		"local_override":  true, "remote_updates": "disabled_fail_closed",
		"trust_keys":     []map[string]any{{"id": "key_1", "fingerprint": "SHA256:f1", "source": "builtin", "added_at": 1, "revoked_at": nil}},
		"last_rejection": map[string]any{"reason": "unknown_key", "at": 0},
	}
	socket := serveFakeDaemonWithParams(t, map[string]any{"rules.status": status, "rules.reset": status}, make(chan map[string]any, 8))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"rules", "status", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	want := "active: version 7 (remote) a7\nprevious: version 6 a6\nlocal overrides: true\nremote updates: disabled_fail_closed\n" +
		"last rejection: unknown_key at 1970-01-01T00:00:00Z\nSHA256:f1  key_1  builtin  valid\n"
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}
	stdout.Reset()
	if code := run([]string{"rules", "reset", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Errorf("reset: exit %d, stderr %q", code, stderr.String())
	}
	if code := run([]string{"rules", "bogus", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitFailure {
		t.Errorf("an unknown subcommand: exit %d", code)
	}
}
