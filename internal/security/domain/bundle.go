package domain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// RejectReason is why a fetched bundle was discarded (REQ-SEC-013, delta
// `2026-10-signed-rule-bundles` decision 2). The empty reason is a bundle that verified.
type RejectReason string

// The reasons, in the order verification meets them.
const (
	RejectUnsigned         RejectReason = "unsigned"
	RejectUnknownKey       RejectReason = "unknown_key"
	RejectInvalidSignature RejectReason = "invalid_signature"
	RejectInvalidBundle    RejectReason = "invalid_bundle"
	RejectDowngrade        RejectReason = "downgrade"
)

// ErrInvalidPublicKey is a public key that is not the standard base64 of 32 bytes.
var ErrInvalidPublicKey = errors.New("invalid public key")

// TrustKey is one row of the trust store (Data Model §2.4e). Only public keys live here.
type TrustKey struct {
	ID          string
	PublicKey   []byte
	Fingerprint string
	AddedAt     int64
	RevokedAt   *int64
}

// Valid is a key with no revocation (API §5.34).
func (k TrustKey) Valid() bool { return k.RevokedAt == nil }

// Fingerprint is DD-016's: `SHA256:` and the unpadded standard base64 of SHA-256 over the key's
// 32 raw bytes.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// ParsePublicKey reads a public key as DD-016 writes it: the standard base64 of 32 raw bytes.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: want the base64 of %d bytes", ErrInvalidPublicKey, ed25519.PublicKeySize)
	}
	return raw, nil
}

// signatureFile is the `.sig` beside a bundle: `{"signatures":[{"fingerprint", "sig"}]}`.
type signatureFile struct {
	Signatures []signatureEntry `json:"signatures"`
}

type signatureEntry struct {
	Fingerprint string `json:"fingerprint"`
	Sig         string `json:"sig"`
}

// VerifyBundle checks a bundle's exact bytes against its signature file and the trust store.
// It verifies when an entry names a valid key and checks out, and answers the most recently
// added such key (DD-016). Otherwise the reason says how it failed: no usable entry is
// `unsigned`, no entry naming a valid key is `unknown_key`, and entries of valid keys none of
// which checks out is `invalid_signature`.
func VerifyBundle(bundle, sig []byte, keys []TrustKey) (TrustKey, RejectReason) {
	var f signatureFile
	if err := json.Unmarshal(sig, &f); err != nil || len(f.Signatures) == 0 {
		return TrustKey{}, RejectUnsigned
	}
	valid := map[string]TrustKey{}
	for _, k := range keys {
		if k.Valid() && len(k.PublicKey) == ed25519.PublicKeySize {
			valid[k.Fingerprint] = k
		}
	}
	var named bool
	var best TrustKey
	var verified bool
	for _, e := range f.Signatures {
		k, ok := valid[e.Fingerprint]
		if !ok {
			continue
		}
		named = true
		raw, err := base64.StdEncoding.DecodeString(e.Sig)
		if err != nil || !ed25519.Verify(k.PublicKey, bundle, raw) {
			continue
		}
		if !verified || k.AddedAt > best.AddedAt {
			best, verified = k, true
		}
	}
	switch {
	case verified:
		return best, ""
	case named:
		return TrustKey{}, RejectInvalidSignature
	default:
		return TrustKey{}, RejectUnknownKey
	}
}

// SignBundle adds priv's signature over bundle to a signature file, creating it when sig is
// empty; an entry the key already had is replaced. `tools/rulesign sign` is this function.
func SignBundle(priv ed25519.PrivateKey, bundle, sig []byte) ([]byte, error) {
	var f signatureFile
	if len(bytes.TrimSpace(sig)) > 0 {
		if err := json.Unmarshal(sig, &f); err != nil {
			return nil, fmt.Errorf("the signature file: %w", err)
		}
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an Ed25519 key")
	}
	entry := signatureEntry{Fingerprint: Fingerprint(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, bundle))}
	out := f.Signatures[:0:0]
	for _, e := range f.Signatures {
		if e.Fingerprint != entry.Fingerprint {
			out = append(out, e)
		}
	}
	f.Signatures = append(out, entry)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// BundleDigest is the hex SHA-256 of a bundle's exact bytes, the `rule_bundles.sha256` of a
// remote row.
func BundleDigest(bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// RulesDigest is the `builtin` row's digest: the SHA-256 of a rule set's canonical JSON, so a
// binary carrying different rules rewrites the row (Data Model §2.4e).
func RulesDigest(s RuleSet) string {
	type entry struct {
		Name, Pattern string
		Group         int `json:",omitempty"`
	}
	var doc struct{ Redaction, Destructive []entry }
	for _, r := range s.Redaction {
		doc.Redaction = append(doc.Redaction, entry{r.Name, r.Expr.String(), r.Group})
	}
	for _, d := range s.Destructive {
		doc.Destructive = append(doc.Destructive, entry{Name: d.Name, Pattern: d.Expr.String()})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		panic(err) // strings and ints always marshal
	}
	return BundleDigest(data)
}
