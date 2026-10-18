package trustseed

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
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
