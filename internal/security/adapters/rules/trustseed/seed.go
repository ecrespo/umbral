// Package trustseed is the trust store's seed built into the binary (DD-016): the project's
// current public keys with the ids every install gives them, the fingerprints of retired keys
// and those of revoked ones. It ships empty until the Tech Lead creates the project key
// offline and adds it by pull request (docs/runbooks/rule-signing.md §4.1).
package trustseed

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/ecrespo/umbral/internal/security/domain"
)

//go:embed seed.json
var seedJSON []byte

var keyID = regexp.MustCompile(`^key_[0-9A-HJKMNP-TV-Z]{26}$`)

type wireSeed struct {
	Keys []struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
	} `json:"keys"`
	Retired []string `json:"retired"`
	Revoked []string `json:"revoked"`
}

// Load returns the embedded seed.
func Load() (domain.Seed, error) { return Parse(seedJSON) }

// Parse reads a seed strictly: every key a `key_` ULID id and the base64 of 32 bytes, and no
// fingerprint both current and revoked.
func Parse(data []byte) (domain.Seed, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wireSeed
	if err := dec.Decode(&w); err != nil {
		return domain.Seed{}, fmt.Errorf("trustseed: %w", err)
	}
	seed := domain.Seed{Retired: w.Retired, Revoked: w.Revoked}
	for _, k := range w.Keys {
		if !keyID.MatchString(k.ID) {
			return domain.Seed{}, fmt.Errorf("trustseed: key id %q is not key_ and a ULID", k.ID)
		}
		pub, err := domain.ParsePublicKey(k.PublicKey)
		if err != nil {
			return domain.Seed{}, fmt.Errorf("trustseed: key %s: %w", k.ID, err)
		}
		if seed.Revokes(domain.Fingerprint(pub)) {
			return domain.Seed{}, fmt.Errorf("trustseed: key %s is also listed as revoked", k.ID)
		}
		seed.Keys = append(seed.Keys, domain.SeedKey{ID: k.ID, PublicKey: pub})
	}
	return seed, nil
}
