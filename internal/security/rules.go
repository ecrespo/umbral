package security

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
)

// RejectedEgressProvider prefixes the `egress_log` row a rejection writes; the reason follows
// (delta `2026-10-signed-rule-bundles`, decision 3).
const RejectedEgressProvider = "umbral-rules:rejected:"

// RuleMetrics counts rejections (REQ-OBS-004). obs.Metrics implements it.
type RuleMetrics interface {
	RuleUpdateRejected(reason string)
}

// RulesConfig wires the rule manager.
type RulesConfig struct {
	Store ports.RuleStore
	Base  ports.RuleBase
	Seed  domain.Seed
	// Builtin is the rules built into the binary, the `builtin` row (version 0).
	Builtin domain.RuleSet
	// Enabled is `[update] rules_check`; off, remote_updates is `disabled_by_config`.
	Enabled bool
	// Host is rules_url's host, written on a rejection's egress row.
	Host string
	// LocalOverride is whether the override directory contributed any rule.
	LocalOverride bool
	// Publish announces `rules.update_rejected`; nil announces nothing.
	Publish  func(ports.RuleUpdateRejected)
	Metrics  RuleMetrics
	NewKeyID func() string
	Now      func() time.Time
	Logger   *slog.Logger
}

// Rules verifies and applies signed bundles and manages the trust store (REQ-SEC-011,
// REQ-SEC-013 to REQ-SEC-016). Every change is written before the rule set changes or anyone
// is told (DD-007), and one lock orders them, so a bundle offered during a revocation sees
// either the store before it or the store after.
type Rules struct {
	cfg RulesConfig
	mu  sync.Mutex
}

// NewRules builds the manager. Call Start before anything else.
func NewRules(cfg RulesConfig) (*Rules, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("security: a RuleStore is required")
	case cfg.Base == nil:
		return nil, errors.New("security: a RuleBase is required")
	case cfg.NewKeyID == nil:
		return nil, errors.New("security: NewKeyID is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Rules{cfg: cfg}, nil
}

func (r *Rules) now() int64 { return r.cfg.Now().UnixMilli() }

// Start applies the seed, writes the `builtin` row and puts the active bundle under the local
// overrides (DD-016, Data Model §2.4e).
func (r *Rules) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var active domain.RuleSet
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		keys, err := tx.Keys()
		if err != nil {
			return err
		}
		at := r.now()
		byFP := map[string]domain.TrustKey{}
		for _, k := range keys {
			byFP[k.Fingerprint] = k
		}
		for _, sk := range r.cfg.Seed.Keys {
			fp := domain.Fingerprint(sk.PublicKey)
			if _, ok := byFP[fp]; ok || r.cfg.Seed.Revokes(fp) {
				continue
			}
			k := domain.TrustKey{ID: sk.ID, PublicKey: sk.PublicKey, Fingerprint: fp, AddedAt: at}
			if err := tx.InsertKey(k); err != nil {
				return err
			}
			if err := tx.KeyEvent(domain.KeyEvent{KeyID: k.ID, Fingerprint: fp, Action: domain.KeySeeded, At: at}); err != nil {
				return err
			}
			byFP[fp] = k
		}
		wasValid := hasValid(byFP)
		for fp, k := range byFP {
			if k.Valid() && r.cfg.Seed.Revokes(fp) {
				if err := r.revoke(tx, k, at); err != nil {
					return err
				}
				k.RevokedAt = &at
				byFP[fp] = k
				if err := tx.KeyEvent(domain.KeyEvent{KeyID: k.ID, Fingerprint: fp, Action: domain.KeySeedRevoked, At: at}); err != nil {
					return err
				}
			}
		}
		// Read after the revocations: revoke writes the state too (the floor it lowers), and a
		// copy read before them would put the old floor back.
		state, err := tx.State()
		if err != nil {
			return err
		}
		if hasValid(byFP) {
			state.EverHadValidKey = true
		} else if wasValid {
			// The seed revoked the last valid key: the store ended up without one.
			state.FailClosed = true
		}
		if err := tx.SetState(state); err != nil {
			return err
		}
		if active, err = r.writeBuiltin(tx, at); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("security: start the rule manager: %w", err)
	}
	r.cfg.Base.SetBase(active)
	return nil
}

func hasValid(keys map[string]domain.TrustKey) bool {
	for _, k := range keys {
		if k.Valid() {
			return true
		}
	}
	return false
}

// writeBuiltin keeps the `builtin` row's digest current, makes it active when nothing is, and
// returns the rules of the active row. A remote row whose bytes no longer match its digest,
// or no longer parse, is not trusted: the built-in rules apply and the reason is logged.
func (r *Rules) writeBuiltin(tx ports.RuleTx, at int64) (domain.RuleSet, error) {
	bundles, err := tx.Bundles()
	if err != nil {
		return domain.RuleSet{}, err
	}
	digest := domain.RulesDigest(r.cfg.Builtin)
	var builtin, active *domain.BundleRecord
	for i := range bundles {
		if bundles[i].Source == domain.SourceBuiltin {
			builtin = &bundles[i]
		}
		if bundles[i].Active {
			active = &bundles[i]
		}
	}
	if builtin == nil || builtin.SHA256 != digest {
		b := domain.BundleRecord{Version: 0, SHA256: digest, Source: domain.SourceBuiltin, InstalledAt: at, Active: active == nil || active.Source == domain.SourceBuiltin}
		if err := tx.PutBundle(b); err != nil {
			return domain.RuleSet{}, err
		}
	}
	if active == nil {
		if err := tx.SetActive(0); err != nil {
			return domain.RuleSet{}, err
		}
		return r.cfg.Builtin, nil
	}
	if active.Source == domain.SourceBuiltin {
		return r.cfg.Builtin, nil
	}
	rules, err := parseStored(*active)
	if err != nil {
		r.cfg.Logger.Error("the active rule bundle cannot be trusted; the built-in rules apply", slog.Int64("version", active.Version), slog.Any("error", err))
		return r.cfg.Builtin, nil
	}
	return rules, nil
}

func parseStored(b domain.BundleRecord) (domain.RuleSet, error) {
	if domain.BundleDigest(b.Content) != b.SHA256 {
		return domain.RuleSet{}, errors.New("its bytes do not match its digest")
	}
	doc, err := domain.ParseRules(b.Content)
	if err != nil {
		return domain.RuleSet{}, err
	}
	return doc.Rules, nil
}

// UpdatesAllowed is false when remote updates are off or failed closed, so the fetcher makes
// no request (REQ-SEC-015).
func (r *Rules) UpdatesAllowed(ctx context.Context) bool {
	st, err := r.Status(ctx)
	return err == nil && st.RemoteUpdates == domain.UpdatesEnabled
}

// Offer verifies a fetched bundle and applies it, or rejects it with its reason (REQ-SEC-011,
// REQ-SEC-013). It implements ports.RuleUpdater.
func (r *Rules) Offer(ctx context.Context, bundle, signature []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		applied  *domain.RuleSet
		rejected *ports.RuleUpdateRejected
	)
	digest := domain.BundleDigest(bundle)
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		applied, rejected = nil, nil
		keys, err := tx.Keys()
		if err != nil {
			return err
		}
		state, err := tx.State()
		if err != nil {
			return err
		}
		if !r.cfg.Enabled || state.FailClosed || !anyValid(keys) {
			r.cfg.Logger.WarnContext(ctx, "a rule bundle was offered while remote updates are off; it was discarded")
			return nil
		}
		bundles, err := tx.Bundles()
		if err != nil {
			return err
		}
		active := activeOf(bundles)
		if digest == active.SHA256 {
			return nil // up to date (DD-016)
		}
		if seen, err := tx.Rejection(digest); err != nil || seen {
			return err // not retried, not counted (REQ-SEC-013)
		}
		key, reason := domain.VerifyBundle(bundle, signature, keys)
		var doc domain.RuleDocument
		if reason == "" {
			var perr error
			doc, perr = domain.ParseRules(bundle)
			switch {
			case perr != nil || doc.Version < 1:
				reason = domain.RejectInvalidBundle
			case doc.Version <= max(active.Version, state.HighestVersion):
				reason = domain.RejectDowngrade
			}
		}
		at := r.now()
		if reason != "" {
			rejected = &ports.RuleUpdateRejected{Reason: reason, Version: doc.Version, Source: domain.SourceRemote}
			return r.reject(tx, state, digest, len(bundle), *rejected, at)
		}
		if err := tx.PutBundle(domain.BundleRecord{
			Version: doc.Version, SHA256: digest, Source: domain.SourceRemote, VerifiedWith: key.ID,
			InstalledAt: at, Content: bundle,
		}); err != nil {
			return err
		}
		if err := tx.SetActive(doc.Version); err != nil {
			return err
		}
		if err := prune(tx); err != nil {
			return err
		}
		state.ConsecutiveFailures = 0
		state.HighestVersion = max(state.HighestVersion, doc.Version)
		if err := tx.SetState(state); err != nil {
			return err
		}
		applied = &doc.Rules
		return nil
	})
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "a rule bundle could not be processed; the rules in force stay", slog.Any("error", err))
		return
	}
	switch {
	case applied != nil:
		r.cfg.Base.SetBase(*applied)
		r.cfg.Logger.InfoContext(ctx, "a verified rule bundle was applied", slog.String("sha256", digest))
	case rejected != nil:
		r.cfg.Logger.WarnContext(ctx, "a rule bundle was rejected; the rules in force stay",
			slog.String("reason", string(rejected.Reason)), slog.Int64("version", rejected.Version))
		if r.cfg.Metrics != nil {
			r.cfg.Metrics.RuleUpdateRejected(string(rejected.Reason))
		}
		if r.cfg.Publish != nil {
			r.cfg.Publish(*rejected)
		}
	}
}

func (r *Rules) reject(tx ports.RuleTx, state domain.UpdateState, digest string, size int, ev ports.RuleUpdateRejected, at int64) error {
	if err := tx.PutRejection(domain.Rejection{SHA256: digest, Reason: ev.Reason, Version: ev.Version, At: at}); err != nil {
		return err
	}
	state.ConsecutiveFailures++
	state.LastRejection, state.LastRejectionAt = ev.Reason, at
	if state.ConsecutiveFailures >= domain.FailClosedAfter {
		state.FailClosed = true
	}
	if err := tx.SetState(state); err != nil {
		return err
	}
	return tx.Egress(llmdomain.EgressRecord{
		Provider: RejectedEgressProvider + string(ev.Reason), Host: r.cfg.Host,
		Bytes: int64(size), PayloadSHA256: digest, CreatedAt: at,
	})
}

func anyValid(keys []domain.TrustKey) bool {
	return slices.ContainsFunc(keys, domain.TrustKey.Valid)
}

func activeOf(bundles []domain.BundleRecord) domain.BundleRecord {
	for _, b := range bundles {
		if b.Active {
			return b
		}
	}
	return domain.BundleRecord{Source: domain.SourceBuiltin}
}

// previousOf is the newest remote row that is not active: what a rollback reinstalls.
func previousOf(bundles []domain.BundleRecord) *domain.BundleRecord {
	for i := range bundles {
		if !bundles[i].Active && bundles[i].Source == domain.SourceRemote {
			return &bundles[i]
		}
	}
	return nil
}

// prune keeps the active row, the previous bundle and `builtin` (Data Model §2.4e).
func prune(tx ports.RuleTx) error {
	bundles, err := tx.Bundles()
	if err != nil {
		return err
	}
	prev := previousOf(bundles)
	for _, b := range bundles {
		if b.Active || b.Source == domain.SourceBuiltin || (prev != nil && b.Version == prev.Version) {
			continue
		}
		if err := tx.DeleteBundle(b.Version); err != nil {
			return err
		}
	}
	return nil
}

// Status is `rules.status`.
func (r *Rules) Status(ctx context.Context) (domain.RulesStatus, error) {
	var st domain.RulesStatus
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		var err error
		st, err = r.status(tx)
		return err
	})
	return st, err
}

func (r *Rules) status(tx ports.RuleTx) (domain.RulesStatus, error) {
	keys, err := tx.Keys()
	if err != nil {
		return domain.RulesStatus{}, err
	}
	bundles, err := tx.Bundles()
	if err != nil {
		return domain.RulesStatus{}, err
	}
	state, err := tx.State()
	if err != nil {
		return domain.RulesStatus{}, err
	}
	st := domain.RulesStatus{
		Active: info(activeOf(bundles)), LocalOverride: r.cfg.LocalOverride,
		LastRejection: state.LastRejection, LastRejectionAt: state.LastRejectionAt,
	}
	if prev := previousOf(bundles); prev != nil {
		p := info(*prev)
		st.Previous = &p
	}
	switch {
	case !r.cfg.Enabled:
		st.RemoteUpdates = domain.UpdatesDisabledByConfig
	case state.FailClosed || !anyValid(keys):
		st.RemoteUpdates = domain.UpdatesFailClosed
	default:
		st.RemoteUpdates = domain.UpdatesEnabled
	}
	for _, k := range keys {
		st.TrustKeys = append(st.TrustKeys, r.keyInfo(k))
	}
	return st, nil
}

func info(b domain.BundleRecord) domain.BundleInfo {
	return domain.BundleInfo{Version: b.Version, SHA256: b.SHA256, Source: b.Source, VerifiedWith: b.VerifiedWith}
}

func (r *Rules) keyInfo(k domain.TrustKey) domain.KeyInfo {
	source := domain.SourceUser
	if r.cfg.Seed.Knows(k.Fingerprint) {
		source = domain.SourceBuiltin
	}
	return domain.KeyInfo{ID: k.ID, Fingerprint: k.Fingerprint, Source: source, AddedAt: k.AddedAt, RevokedAt: k.RevokedAt}
}

// ListKeys is `rules.key.list`.
func (r *Rules) ListKeys(ctx context.Context) ([]domain.KeyInfo, error) {
	st, err := r.Status(ctx)
	return st.TrustKeys, err
}

// AddKey is `rules.key.add` (REQ-SEC-014): the confirmed fingerprint must be the key's; a key
// the seed revokes is refused; a revoked key comes back and a valid one is unchanged, both
// recorded. Any add clears the fail-closed latch, and it is the only thing that does
// (REQ-SEC-015).
func (r *Rules) AddKey(ctx context.Context, publicKey, confirmFingerprint string) (domain.KeyInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out domain.KeyInfo
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		k, err := r.add(tx, publicKey, confirmFingerprint, r.now())
		out = r.keyInfo(k)
		return err
	})
	return out, err
}

func (r *Rules) add(tx ports.RuleTx, publicKey, confirm string, at int64) (domain.TrustKey, error) {
	pub, err := domain.ParsePublicKey(publicKey)
	if err != nil {
		return domain.TrustKey{}, err
	}
	fp := domain.Fingerprint(pub)
	if confirm != fp {
		return domain.TrustKey{}, domain.ErrFingerprintMismatch
	}
	if r.cfg.Seed.Revokes(fp) {
		return domain.TrustKey{}, domain.ErrKeyRevokedBySeed
	}
	keys, err := tx.Keys()
	if err != nil {
		return domain.TrustKey{}, err
	}
	k := domain.TrustKey{ID: r.cfg.NewKeyID(), PublicKey: pub, Fingerprint: fp, AddedAt: at}
	action := domain.KeyAdded
	if i := slices.IndexFunc(keys, func(e domain.TrustKey) bool { return e.Fingerprint == fp }); i >= 0 {
		k = keys[i]
		action = domain.KeyUnchanged
		if !k.Valid() {
			action = domain.KeyReadded
			if err := tx.SetRevoked(k.ID, nil); err != nil {
				return domain.TrustKey{}, err
			}
			k.RevokedAt = nil
		}
	} else if err := tx.InsertKey(k); err != nil {
		return domain.TrustKey{}, err
	}
	if err := tx.KeyEvent(domain.KeyEvent{KeyID: k.ID, Fingerprint: fp, Action: action, At: at}); err != nil {
		return domain.TrustKey{}, err
	}
	state, err := tx.State()
	if err != nil {
		return domain.TrustKey{}, err
	}
	state.FailClosed, state.ConsecutiveFailures, state.EverHadValidKey = false, 0, true
	return k, tx.SetState(state)
}

// RemoveKey is `rules.key.remove`: it revokes the key, discards the bundles it verified and
// forgets downgrade rejections; removing the last valid key needs force and fails updates
// closed (REQ-SEC-014, REQ-SEC-015).
func (r *Rules) RemoveKey(ctx context.Context, fingerprint string, force bool) (domain.KeyInfo, domain.BundleInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		out    domain.KeyInfo
		active domain.BundleInfo
		base   *domain.RuleSet
	)
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		k, err := r.find(tx, fingerprint)
		if err != nil {
			return err
		}
		at := r.now()
		if k.Valid() {
			keys, err := tx.Keys()
			if err != nil {
				return err
			}
			last := slices.IndexFunc(keys, func(e domain.TrustKey) bool { return e.Valid() && e.ID != k.ID }) < 0
			if last && !force {
				return domain.ErrLastValidKey
			}
			if base, err = r.revokeAndReselect(tx, k, at); err != nil {
				return err
			}
			k.RevokedAt = &at
			if last {
				state, err := tx.State()
				if err != nil {
					return err
				}
				state.FailClosed = true
				if err := tx.SetState(state); err != nil {
					return err
				}
			}
		}
		out = r.keyInfo(k)
		bundles, err := tx.Bundles()
		active = info(activeOf(bundles))
		return err
	})
	if err == nil && base != nil {
		r.cfg.Base.SetBase(*base)
	}
	return out, active, err
}

// RotateKey is `rules.key.rotate`: add the new key, then revoke the old one with remove's
// consequences, in one transaction.
func (r *Rules) RotateKey(ctx context.Context, fingerprint, publicKey, confirmFingerprint string) (domain.KeyInfo, domain.KeyInfo, domain.BundleInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		added, revoked domain.KeyInfo
		active         domain.BundleInfo
		base           *domain.RuleSet
	)
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		old, err := r.find(tx, fingerprint)
		if err != nil {
			return err
		}
		at := r.now()
		k, err := r.add(tx, publicKey, confirmFingerprint, at)
		if err != nil {
			return err
		}
		if old.Valid() && old.ID != k.ID {
			if base, err = r.revokeAndReselect(tx, old, at); err != nil {
				return err
			}
			old.RevokedAt = &at
		}
		added, revoked = r.keyInfo(k), r.keyInfo(old)
		bundles, err := tx.Bundles()
		active = info(activeOf(bundles))
		return err
	})
	if err == nil && base != nil {
		r.cfg.Base.SetBase(*base)
	}
	return added, revoked, active, err
}

func (r *Rules) find(tx ports.RuleTx, fingerprint string) (domain.TrustKey, error) {
	keys, err := tx.Keys()
	if err != nil {
		return domain.TrustKey{}, err
	}
	i := slices.IndexFunc(keys, func(k domain.TrustKey) bool { return k.Fingerprint == fingerprint })
	if i < 0 {
		return domain.TrustKey{}, domain.ErrUnknownKey
	}
	return keys[i], nil
}

// revokeAndReselect revokes k and, when that discarded the active bundle, returns the rules
// now in force.
func (r *Rules) revokeAndReselect(tx ports.RuleTx, k domain.TrustKey, at int64) (*domain.RuleSet, error) {
	before, err := tx.Bundles()
	if err != nil {
		return nil, err
	}
	if err := r.revoke(tx, k, at); err != nil {
		return nil, err
	}
	if err := tx.KeyEvent(domain.KeyEvent{KeyID: k.ID, Fingerprint: k.Fingerprint, Action: domain.KeyRevoked, At: at}); err != nil {
		return nil, err
	}
	after, err := tx.Bundles()
	if err != nil {
		return nil, err
	}
	if activeOf(before).SHA256 == activeOf(after).SHA256 {
		return nil, nil
	}
	rules, err := r.rulesOf(activeOf(after))
	return &rules, err
}

// revoke sets revoked_at, deletes the bundles k verified and forgets downgrade rejections. If
// the active row went with them, the newest remaining remote row, or `builtin`, becomes
// active (Data Model §2.4e).
func (r *Rules) revoke(tx ports.RuleTx, k domain.TrustKey, at int64) error {
	if err := tx.SetRevoked(k.ID, &at); err != nil {
		return err
	}
	bundles, err := tx.Bundles()
	if err != nil {
		return err
	}
	lostActive := false
	for _, b := range bundles {
		if b.Source == domain.SourceRemote && b.VerifiedWith == k.ID {
			lostActive = lostActive || b.Active
			if err := tx.DeleteBundle(b.Version); err != nil {
				return err
			}
		}
	}
	if err := tx.DeleteRejections(domain.RejectDowngrade); err != nil {
		return err
	}
	remaining, err := tx.Bundles()
	if err != nil {
		return err
	}
	// The floor falls to what the surviving bundles hold: a stolen key's enormous version
	// must not lock every legitimate bundle out (DD-016).
	state, err := tx.State()
	if err != nil {
		return err
	}
	state.HighestVersion = 0
	for _, b := range remaining {
		state.HighestVersion = max(state.HighestVersion, b.Version)
	}
	if err := tx.SetState(state); err != nil {
		return err
	}
	if !lostActive {
		return nil
	}
	for _, b := range remaining {
		if b.Source == domain.SourceRemote {
			return tx.SetActive(b.Version)
		}
	}
	return tx.SetActive(0)
}

func (r *Rules) rulesOf(b domain.BundleRecord) (domain.RuleSet, error) {
	if b.Source == domain.SourceBuiltin {
		return r.cfg.Builtin, nil
	}
	return parseStored(b)
}

// Rollback is `rules.rollback`: the previous bundle becomes active, offline and without a key;
// the bundle left behind is deleted and remembered so the next check does not reinstall it
// (REQ-SEC-016).
func (r *Rules) Rollback(ctx context.Context) (domain.RulesStatus, error) {
	return r.switchTo(ctx, func(tx ports.RuleTx, bundles []domain.BundleRecord, at int64) (domain.BundleRecord, error) {
		prev := previousOf(bundles)
		if prev == nil {
			return domain.BundleRecord{}, domain.ErrNoPreviousBundle
		}
		if cur := activeOf(bundles); cur.Source == domain.SourceRemote {
			if err := tx.DeleteBundle(cur.Version); err != nil {
				return domain.BundleRecord{}, err
			}
			if err := tx.PutRejection(domain.Rejection{SHA256: cur.SHA256, Reason: domain.SkipRolledBack, Version: cur.Version, At: at}); err != nil {
				return domain.BundleRecord{}, err
			}
		}
		return *prev, tx.DeleteRejection(prev.SHA256)
	})
}

// Reset is `rules.reset`: the rules built into the binary become active, offline and without
// a key; the remote rows stay for a later rollback (REQ-SEC-016).
func (r *Rules) Reset(ctx context.Context) (domain.RulesStatus, error) {
	return r.switchTo(ctx, func(tx ports.RuleTx, bundles []domain.BundleRecord, at int64) (domain.BundleRecord, error) {
		if cur := activeOf(bundles); cur.Source == domain.SourceRemote {
			if err := tx.PutRejection(domain.Rejection{SHA256: cur.SHA256, Reason: domain.SkipReset, Version: cur.Version, At: at}); err != nil {
				return domain.BundleRecord{}, err
			}
		}
		return domain.BundleRecord{Version: 0, Source: domain.SourceBuiltin}, nil
	})
}

func (r *Rules) switchTo(ctx context.Context, pick func(ports.RuleTx, []domain.BundleRecord, int64) (domain.BundleRecord, error)) (domain.RulesStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		st    domain.RulesStatus
		rules domain.RuleSet
	)
	err := r.cfg.Store.Update(ctx, func(tx ports.RuleTx) error {
		bundles, err := tx.Bundles()
		if err != nil {
			return err
		}
		target, err := pick(tx, bundles, r.now())
		if err != nil {
			return err
		}
		if rules, err = r.rulesOf(target); err != nil {
			return fmt.Errorf("the bundle to reinstall: %w", err)
		}
		if err := tx.SetActive(target.Version); err != nil {
			return err
		}
		st, err = r.status(tx)
		return err
	})
	if err != nil {
		return domain.RulesStatus{}, err
	}
	r.cfg.Base.SetBase(rules)
	return st, nil
}
