package domain

import "errors"

// Sources of a `rule_bundles` row and of a trust key (Data Model §2.4e, API §5.34).
const (
	SourceBuiltin = "builtin"
	SourceRemote  = "remote"
	SourceUser    = "user"
)

// Rejection reasons that are not verification failures: a bundle the user rolled back or reset
// away from is remembered so the next check does not reinstall it (delta
// `2026-10-signed-rule-bundles`, decision 6).
const (
	SkipRolledBack RejectReason = "rolled_back"
	SkipReset      RejectReason = "reset"
)

// The values of `rules.status`'s `remote_updates`.
const (
	UpdatesEnabled          = "enabled"
	UpdatesDisabledByConfig = "disabled_by_config"
	UpdatesFailClosed       = "disabled_fail_closed"
)

// FailClosedAfter is REQ-SEC-015's three consecutive failed verifications.
const FailClosedAfter = 3

// Trust-store changes as `trust_key_events` records them (REQ-SEC-014).
const (
	KeySeeded      = "seeded"
	KeySeedRevoked = "seed_revoked"
	KeyAdded       = "added"
	KeyReadded     = "readded"
	KeyUnchanged   = "unchanged"
	KeyRevoked     = "revoked"
)

var (
	// ErrFingerprintMismatch is a `confirm_fingerprint` that is not the key's.
	ErrFingerprintMismatch = errors.New("the confirmed fingerprint is not the key's")
	// ErrKeyRevokedBySeed is an add of a fingerprint the binary's seed lists as revoked.
	ErrKeyRevokedBySeed = errors.New("the key is revoked by this build")
	// ErrUnknownKey is a fingerprint the trust store does not hold.
	ErrUnknownKey = errors.New("no such trust key")
	// ErrLastValidKey is a removal of the last valid key without force (REQ-SEC-014).
	ErrLastValidKey = errors.New("it is the last valid key; force is required")
	// ErrNoPreviousBundle is a rollback with nothing to return to.
	ErrNoPreviousBundle = errors.New("there is no previous bundle")
)

// BundleRecord is one `rule_bundles` row. Content is the bundle's exact bytes, nil for the
// built-in rules.
type BundleRecord struct {
	Version      int64
	SHA256       string
	Source       string
	VerifiedWith string
	InstalledAt  int64
	Active       bool
	Content      []byte
}

// Rejection is a bundle not to retry (`rule_rejections`).
type Rejection struct {
	SHA256  string
	Reason  RejectReason
	Version int64
	At      int64
}

// UpdateState is `rule_update_state`'s one row.
type UpdateState struct {
	ConsecutiveFailures int
	FailClosed          bool
	EverHadValidKey     bool
	// HighestVersion is the highest version ever applied: with the active row's, the floor a
	// new bundle must exceed, so a reset does not reopen older signed bundles. Only a
	// revocation lowers it (DD-016).
	HighestVersion      int64
	LastRejection       RejectReason
	LastRejectionAt     int64
}

// KeyEvent is one `trust_key_events` row.
type KeyEvent struct {
	KeyID       string
	Fingerprint string
	Action      string
	At          int64
}

// SeedKey is a key the binary embeds, with the id every install gives it (API §3).
type SeedKey struct {
	ID        string
	PublicKey []byte
}

// Seed is the trust seed built into the binary (DD-016): the current keys, the retired
// fingerprints (only ever reported as `builtin`) and the revoked ones.
type Seed struct {
	Keys    []SeedKey
	Retired []string
	Revoked []string
}

// Knows reports whether the seed carries a fingerprint as current, retired or revoked, which
// makes the key's source `builtin` (API §5.34).
func (s Seed) Knows(fingerprint string) bool {
	for _, k := range s.Keys {
		if Fingerprint(k.PublicKey) == fingerprint {
			return true
		}
	}
	for _, fp := range append(append([]string(nil), s.Retired...), s.Revoked...) {
		if fp == fingerprint {
			return true
		}
	}
	return false
}

// Revokes reports whether the seed lists a fingerprint as revoked.
func (s Seed) Revokes(fingerprint string) bool {
	for _, fp := range s.Revoked {
		if fp == fingerprint {
			return true
		}
	}
	return false
}

// BundleInfo is a bundle as `rules.status` reports it.
type BundleInfo struct {
	Version      int64
	SHA256       string
	Source       string
	VerifiedWith string
}

// KeyInfo is a trust key as the wire reports it.
type KeyInfo struct {
	ID          string
	Fingerprint string
	Source      string
	AddedAt     int64
	RevokedAt   *int64
}

// RulesStatus is `rules.status`'s result.
type RulesStatus struct {
	Active          BundleInfo
	Previous        *BundleInfo
	LocalOverride   bool
	RemoteUpdates   string
	TrustKeys       []KeyInfo
	LastRejection   RejectReason
	LastRejectionAt int64
}
