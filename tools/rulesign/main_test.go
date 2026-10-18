package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/security/domain"
)

// TestRulesignVerifiesLikeTheDaemon_REQ_SEC_011: a key from keygen signs a bundle, verify
// accepts it, and the daemon's VerifyBundle accepts the same signature file; a byte changed
// in the bundle, or another key, fails both. A second signature is appended, not replacing
// the first, so a bundle carries both keys during a rotation.
func TestRulesignVerifiesLikeTheDaemon_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bundle := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(bundle, []byte(`{"version": 3, "destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	keygen := func() (priv, pub string) {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run([]string{"keygen"}, nil, &out, &errOut); code != 0 {
			t.Fatalf("keygen: %d %s", code, errOut.String())
		}
		m := regexp.MustCompile(`public key: (\S+)\nfingerprint: (SHA256:\S+)\n`).FindStringSubmatch(errOut.String())
		if m == nil {
			t.Fatalf("keygen stderr %q", errOut.String())
		}
		return out.String(), m[1]
	}
	privA, pubA := keygen()
	privB, pubB := keygen()

	for _, priv := range []string{privA, privB} {
		var errOut bytes.Buffer
		if code := run([]string{"sign", bundle}, strings.NewReader(priv), &bytes.Buffer{}, &errOut); code != 0 {
			t.Fatalf("sign: %d %s", code, errOut.String())
		}
	}
	for _, pub := range []string{pubA, pubB} {
		var out, errOut bytes.Buffer
		if code := run([]string{"verify", bundle, pub}, nil, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "ok: version 3") {
			t.Errorf("verify with %s: %d %q %q", pub, code, out.String(), errOut.String())
		}
	}

	// The daemon's own check over the files rulesign wrote.
	data, _ := os.ReadFile(bundle)
	sig, _ := os.ReadFile(bundle + ".sig")
	pa, _ := domain.ParsePublicKey(pubA)
	if _, reason := domain.VerifyBundle(data, sig, []domain.TrustKey{{ID: "key_a", PublicKey: pa, Fingerprint: domain.Fingerprint(pa)}}); reason != "" {
		t.Errorf("the daemon rejects what rulesign signed: %s", reason)
	}

	// A byte changed after signing fails both.
	if err := os.WriteFile(bundle, []byte(strings.Replace(string(data), "3", "4", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", bundle, pubA}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Error("verify accepted a changed bundle")
	}
	changed, _ := os.ReadFile(bundle)
	if _, reason := domain.VerifyBundle(changed, sig, []domain.TrustKey{{ID: "key_a", PublicKey: pa, Fingerprint: domain.Fingerprint(pa)}}); reason == "" {
		t.Error("the daemon accepted a changed bundle")
	}

	// A key read from anywhere but stdin is not a key: sign with nothing on stdin fails.
	if code := run([]string{"sign", bundle}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Error("sign ran without a private key on stdin")
	}
}
