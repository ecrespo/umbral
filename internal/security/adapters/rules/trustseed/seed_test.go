package trustseed

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/security/domain"
)

// TestTheEmbeddedSeedParses_REQ_SEC_014: the seed the binary carries is well formed, so a bad
// edit to seed.json fails the build's tests rather than every daemon's start.
func TestTheEmbeddedSeedParses_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

// TestTheEmbeddedSeedCarriesTheProjectKey_REQ_SEC_011: the binary trusts exactly the project's
// key, and the three places that publish it agree. An emptied seed, a swapped key or a README
// naming another fingerprint would otherwise pass every test (runbook §5 was a yearly manual
// check).
func TestTheEmbeddedSeedCarriesTheProjectKey_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	const id, label = "key_01M7NYMYDJTK4FGC17YAJ1G6XG", "2026a"
	seed, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(seed.Keys) != 1 || seed.Keys[0].ID != id || len(seed.Revoked) != 0 {
		t.Fatalf("the seed = %d keys, %d revoked; want the one key %s and none revoked", len(seed.Keys), len(seed.Revoked), id)
	}
	key := base64.StdEncoding.EncodeToString(seed.Keys[0].PublicKey)
	fingerprint := domain.Fingerprint(seed.Keys[0].PublicKey)

	pub, err := os.ReadFile("umbral-rules-" + label + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if want := "public key: " + key + "\nfingerprint: " + fingerprint + "\n"; string(pub) != want {
		t.Errorf("umbral-rules-%s.pub = %q, want what rulesign keygen prints for the seeded key, %q", label, pub, want)
	}
	readme, err := os.ReadFile("../../../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "umbral-rules-"+label) || !strings.Contains(string(readme), fingerprint) {
		t.Errorf("README.md does not publish umbral-rules-%s with the seeded key's fingerprint %s", label, fingerprint)
	}
}

func TestParseRefusesAMalformedSeed_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)
	id := "key_01K5ABCDEFGHJKMNPQRSTVWXYZ"
	good := fmt.Sprintf(`{"keys":[{"id":%q,"public_key":%q}],"retired":[],"revoked":[]}`, id, b64)
	seed, err := Parse([]byte(good))
	if err != nil || len(seed.Keys) != 1 || domain.Fingerprint(seed.Keys[0].PublicKey) != domain.Fingerprint(pub) {
		t.Fatalf("a good seed: %+v %v", seed, err)
	}
	for name, doc := range map[string]string{
		"an unknown member":   `{"keys":[],"extra":1}`,
		"an id without key_":  fmt.Sprintf(`{"keys":[{"id":"k1","public_key":%q}]}`, b64),
		"a short key":         fmt.Sprintf(`{"keys":[{"id":%q,"public_key":"AAAA"}]}`, id),
		"current and revoked": fmt.Sprintf(`{"keys":[{"id":%q,"public_key":%q}],"revoked":[%q]}`, id, b64, domain.Fingerprint(pub)),
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
