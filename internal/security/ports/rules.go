package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/domain"
)

// RuleEgress records every request the rule fetcher makes (REQ-SEC-011, Art. 4): the same
// `egress_log` the model gateway writes.
type RuleEgress interface {
	Record(ctx context.Context, rec llmdomain.EgressRecord) error
}

// RuleUpdater is handed every bundle the fetcher brought back, with its signature, and decides
// whether it is applied: signature, trust store and version (REQ-SEC-011, REQ-SEC-013). The
// fetcher itself never applies anything.
type RuleUpdater interface {
	Offer(ctx context.Context, bundle, signature []byte)
}

// RuleStore holds the trust store, the bundle history and the update state (Data Model
// §2.4e). Update runs fn in one transaction, so a rejection, its count and its egress row, or
// a revocation and the bundles it discards, are written together or not at all.
type RuleStore interface {
	Update(ctx context.Context, fn func(RuleTx) error) error
}

// RuleTx is what one transaction of RuleStore can read and write.
type RuleTx interface {
	Keys() ([]domain.TrustKey, error)
	InsertKey(k domain.TrustKey) error
	SetRevoked(id string, at *int64) error
	KeyEvent(e domain.KeyEvent) error
	// Bundles is every row, newest version first.
	Bundles() ([]domain.BundleRecord, error)
	PutBundle(b domain.BundleRecord) error
	DeleteBundle(version int64) error
	// SetActive makes one row active and every other inactive.
	SetActive(version int64) error
	Rejection(sha256 string) (bool, error)
	PutRejection(r domain.Rejection) error
	DeleteRejection(sha256 string) error
	DeleteRejections(reason domain.RejectReason) error
	State() (domain.UpdateState, error)
	SetState(s domain.UpdateState) error
	// Egress writes an `egress_log` row in the same transaction (REQ-SEC-013).
	Egress(rec llmdomain.EgressRecord) error
}

// RuleBase is the active rule set's base, which a verified bundle, a rollback or a reset
// replaces under the local overrides.
type RuleBase interface {
	SetBase(base domain.RuleSet)
}

// KindRuleUpdateRejected is API Spec §6's `rules.update_rejected`.
const KindRuleUpdateRejected bus.Kind = "rules.update_rejected"

// RuleUpdateRejected is published after a rejection is written (REQ-SEC-013, DD-007).
type RuleUpdateRejected struct {
	Reason  domain.RejectReason
	Version int64
	Source  string
}

// EventKind implements bus.Event.
func (RuleUpdateRejected) EventKind() bus.Kind { return KindRuleUpdateRejected }

// RulesService is `rules.*` (API §5.34).
type RulesService interface {
	Status(ctx context.Context) (domain.RulesStatus, error)
	ListKeys(ctx context.Context) ([]domain.KeyInfo, error)
	AddKey(ctx context.Context, publicKey, confirmFingerprint string) (domain.KeyInfo, error)
	RemoveKey(ctx context.Context, fingerprint string, force bool) (domain.KeyInfo, domain.BundleInfo, error)
	RotateKey(ctx context.Context, fingerprint, publicKey, confirmFingerprint string) (added, revoked domain.KeyInfo, active domain.BundleInfo, err error)
	Rollback(ctx context.Context) (domain.RulesStatus, error)
	Reset(ctx context.Context) (domain.RulesStatus, error)
}
