package domain

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func newKey(t *testing.T, id string, addedAt int64) (TrustKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return TrustKey{ID: id, PublicKey: pub, Fingerprint: Fingerprint(pub), AddedAt: addedAt}, priv
}

func sign(t *testing.T, bundle, sig []byte, priv ed25519.PrivateKey) []byte {
	t.Helper()
	out, err := SignBundle(priv, bundle, sig)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBundleRequiresValidSignature_REQ_SEC_011: a bundle is applied only when an entry of its
// signature file names a valid key and checks out over the bundle's exact bytes; each way of
// failing has its own reason (delta `2026-10-signed-rule-bundles`, decision 2).
func TestBundleRequiresValidSignature_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	trusted, priv := newKey(t, "key_a", 1)
	_, stranger := newKey(t, "key_x", 1)
	bundle := []byte(`{"version": 3}`)
	good := sign(t, bundle, nil, priv)

	revoked := trusted
	at := int64(5)
	revoked.RevokedAt = &at

	tampered := append([]byte(nil), bundle...)
	tampered[len(tampered)-2] = '4'

	var wrong signatureFile
	if err := json.Unmarshal(good, &wrong); err != nil {
		t.Fatal(err)
	}
	wrong.Signatures[0].Sig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	forged, _ := json.Marshal(wrong)

	for name, tc := range map[string]struct {
		bundle, sig []byte
		keys        []TrustKey
		want        RejectReason
	}{
		"signed by a trusted key":     {bundle, good, []TrustKey{trusted}, ""},
		"no signature file content":   {bundle, nil, []TrustKey{trusted}, RejectUnsigned},
		"an empty list":               {bundle, []byte(`{"signatures": []}`), []TrustKey{trusted}, RejectUnsigned},
		"not a signature file":        {bundle, []byte(`[1,2]`), []TrustKey{trusted}, RejectUnsigned},
		"signed by an unknown key":    {bundle, sign(t, bundle, nil, stranger), []TrustKey{trusted}, RejectUnknownKey},
		"signed by a revoked key":     {bundle, good, []TrustKey{revoked}, RejectUnknownKey},
		"a forged signature":          {bundle, forged, []TrustKey{trusted}, RejectInvalidSignature},
		"bytes changed after signing": {tampered, good, []TrustKey{trusted}, RejectInvalidSignature},
	} {
		key, reason := VerifyBundle(tc.bundle, tc.sig, tc.keys)
		if reason != tc.want {
			t.Errorf("%s: reason %q, want %q", name, reason, tc.want)
		}
		if tc.want == "" && key.ID != trusted.ID {
			t.Errorf("%s: verified with %q", name, key.ID)
		}
	}
}

// TestAnySignatureOfAValidKeyVerifies_REQ_SEC_011: during a rotation a bundle carries two
// signatures; it verifies when any entry of a valid key checks out, and verified_with is the
// most recently added of those keys (DD-016).
func TestAnySignatureOfAValidKeyVerifies_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	older, olderPriv := newKey(t, "key_old", 10)
	newer, newerPriv := newKey(t, "key_new", 20)
	_, stranger := newKey(t, "key_x", 1)
	bundle := []byte(`{"version": 4}`)
	both := sign(t, bundle, sign(t, bundle, sign(t, bundle, nil, stranger), olderPriv), newerPriv)

	if key, reason := VerifyBundle(bundle, both, []TrustKey{older, newer}); reason != "" || key.ID != newer.ID {
		t.Errorf("both keys valid: %q %q, want verified with the newer key", key.ID, reason)
	}
	if key, reason := VerifyBundle(bundle, both, []TrustKey{older}); reason != "" || key.ID != older.ID {
		t.Errorf("only the older key known: %q %q", key.ID, reason)
	}
	// An entry that fails does not spoil one that checks out.
	var f signatureFile
	if err := json.Unmarshal(both, &f); err != nil {
		t.Fatal(err)
	}
	for i := range f.Signatures {
		if f.Signatures[i].Fingerprint == newer.Fingerprint {
			f.Signatures[i].Sig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		}
	}
	spoiled, _ := json.Marshal(f)
	if key, reason := VerifyBundle(bundle, spoiled, []TrustKey{older, newer}); reason != "" || key.ID != older.ID {
		t.Errorf("one bad entry beside a good one: %q %q", key.ID, reason)
	}
}

func TestFingerprintAndPublicKeyFormat_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	k, _ := newKey(t, "key_a", 1)
	if !strings.HasPrefix(k.Fingerprint, "SHA256:") || strings.HasSuffix(k.Fingerprint, "=") || len(k.Fingerprint) != len("SHA256:")+43 {
		t.Errorf("fingerprint %q, want SHA256: and 43 unpadded base64 characters", k.Fingerprint)
	}
	pub, err := ParsePublicKey(base64.StdEncoding.EncodeToString(k.PublicKey))
	if err != nil || Fingerprint(pub) != k.Fingerprint {
		t.Errorf("round trip: %v", err)
	}
	for _, bad := range []string{"", "!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted", bad)
		}
	}
}

func TestBuiltinDigestIsStable_REQ_SEC_016(t *testing.T) {
	t.Parallel()
	a, b := RulesDigest(BuiltinRules()), RulesDigest(BuiltinRules())
	if a != b || len(a) != 64 {
		t.Fatalf("digest %q %q", a, b)
	}
	changed := BuiltinRules()
	changed.Destructive = changed.Destructive[1:]
	if RulesDigest(changed) == a {
		t.Error("a change to the rules left the digest alone")
	}
}
