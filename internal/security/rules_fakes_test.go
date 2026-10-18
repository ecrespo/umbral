package security

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
)

// memRules is a RuleStore whose Update is all-or-nothing, like the SQL one.
type memRules struct {
	mu   sync.Mutex
	data memData
}

type memData struct {
	keys       []domain.TrustKey
	bundles    map[int64]domain.BundleRecord
	rejections map[string]domain.Rejection
	state      domain.UpdateState
	events     []domain.KeyEvent
	egress     []llmdomain.EgressRecord
}

func (d memData) clone() memData {
	c := d
	c.keys = slices.Clone(d.keys)
	c.bundles = maps.Clone(d.bundles)
	c.rejections = maps.Clone(d.rejections)
	c.events = slices.Clone(d.events)
	c.egress = slices.Clone(d.egress)
	return c
}

func newMemRules() *memRules {
	return &memRules{data: memData{bundles: map[int64]domain.BundleRecord{}, rejections: map[string]domain.Rejection{}}}
}

func (m *memRules) Update(_ context.Context, fn func(ports.RuleTx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{d: m.data.clone()}
	if err := fn(tx); err != nil {
		return err
	}
	m.data = tx.d
	return nil
}

func (m *memRules) snapshot() memData {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data.clone()
}

type memTx struct{ d memData }

func (t *memTx) Keys() ([]domain.TrustKey, error) { return slices.Clone(t.d.keys), nil }
func (t *memTx) InsertKey(k domain.TrustKey) error {
	t.d.keys = append(t.d.keys, k)
	return nil
}

func (t *memTx) SetRevoked(id string, at *int64) error {
	for i := range t.d.keys {
		if t.d.keys[i].ID == id {
			t.d.keys[i].RevokedAt = at
			return nil
		}
	}
	return fmt.Errorf("no key %s", id)
}

func (t *memTx) KeyEvent(e domain.KeyEvent) error {
	t.d.events = append(t.d.events, e)
	return nil
}

func (t *memTx) Bundles() ([]domain.BundleRecord, error) {
	out := slices.Collect(maps.Values(t.d.bundles))
	slices.SortFunc(out, func(a, b domain.BundleRecord) int { return int(b.Version - a.Version) })
	return out, nil
}

func (t *memTx) PutBundle(b domain.BundleRecord) error {
	if b.Source == domain.SourceRemote && (b.VerifiedWith == "" || b.Content == nil) {
		return fmt.Errorf("a remote bundle needs verified_with and content")
	}
	t.d.bundles[b.Version] = b
	return nil
}

func (t *memTx) DeleteBundle(v int64) error {
	delete(t.d.bundles, v)
	return nil
}

func (t *memTx) SetActive(v int64) error {
	if _, ok := t.d.bundles[v]; !ok {
		return fmt.Errorf("no bundle %d", v)
	}
	for k, b := range t.d.bundles {
		b.Active = k == v
		t.d.bundles[k] = b
	}
	return nil
}

func (t *memTx) Rejection(sha string) (bool, error) {
	_, ok := t.d.rejections[sha]
	return ok, nil
}

func (t *memTx) PutRejection(r domain.Rejection) error {
	t.d.rejections[r.SHA256] = r
	return nil
}

func (t *memTx) DeleteRejection(sha string) error {
	delete(t.d.rejections, sha)
	return nil
}

func (t *memTx) DeleteRejections(reason domain.RejectReason) error {
	maps.DeleteFunc(t.d.rejections, func(_ string, r domain.Rejection) bool { return r.Reason == reason })
	return nil
}

func (t *memTx) State() (domain.UpdateState, error) { return t.d.state, nil }
func (t *memTx) SetState(s domain.UpdateState) error {
	t.d.state = s
	return nil
}

func (t *memTx) Egress(rec llmdomain.EgressRecord) error {
	t.d.egress = append(t.d.egress, rec)
	return nil
}

type recordingBase struct {
	mu  sync.Mutex
	set []domain.RuleSet
}

func (b *recordingBase) SetBase(s domain.RuleSet) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.set = append(b.set, s)
}

func (b *recordingBase) last() domain.RuleSet {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.set[len(b.set)-1]
}

type countingMetrics struct {
	mu  sync.Mutex
	got []string
}

func (m *countingMetrics) RuleUpdateRejected(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.got = append(m.got, reason)
}

// signer is a project key: its public half for the seed or an add, its private half to sign.
type signer struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{pub, priv}
}

func (s signer) b64() string { return base64.StdEncoding.EncodeToString(s.pub) }
func (s signer) fp() string  { return domain.Fingerprint(s.pub) }

func (s signer) sign(t *testing.T, bundle []byte, more ...signer) []byte {
	t.Helper()
	sig, err := domain.SignBundle(s.priv, bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range more {
		if sig, err = domain.SignBundle(o.priv, bundle, sig); err != nil {
			t.Fatal(err)
		}
	}
	return sig
}

// bundleOf is a valid rule bundle of a version whose one destructive pattern names it.
func bundleOf(version int) []byte {
	return fmt.Appendf(nil, `{"version": %d, "destructive": [{"name": "v%d", "pattern": "^v%d( |$)"}]}`, version, version, version)
}

type rulesRig struct {
	r       *Rules
	store   *memRules
	base    *recordingBase
	metrics *countingMetrics
	events  chan ports.RuleUpdateRejected
	clock   *atomic.Int64
}

func newRulesRig(t *testing.T, seed domain.Seed, configure ...func(*RulesConfig)) *rulesRig {
	t.Helper()
	rig := &rulesRig{store: newMemRules(), base: &recordingBase{}, metrics: &countingMetrics{}, events: make(chan ports.RuleUpdateRejected, 16), clock: &atomic.Int64{}}
	rig.clock.Store(1_000)
	var ids atomic.Int64
	cfg := RulesConfig{
		Store: rig.store, Base: rig.base, Seed: seed, Builtin: domain.BuiltinRules(), Enabled: true,
		Host: "rules.example", Metrics: rig.metrics,
		Publish:  func(e ports.RuleUpdateRejected) { rig.events <- e },
		NewKeyID: func() string { return fmt.Sprintf("key_%026d", ids.Add(1)) },
		Now:      func() time.Time { return time.UnixMilli(rig.clock.Add(1)) },
	}
	for _, c := range configure {
		c(&cfg)
	}
	r, err := NewRules(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	rig.r = r
	return rig
}

// restart builds a new manager over the same store, as a daemon restart does.
func (rig *rulesRig) restart(t *testing.T, seed domain.Seed) *rulesRig {
	t.Helper()
	next := *rig
	next.base = &recordingBase{}
	r, err := NewRules(RulesConfig{
		Store: rig.store, Base: next.base, Seed: seed, Builtin: domain.BuiltinRules(), Enabled: true,
		Host: "rules.example", Metrics: rig.metrics, Publish: func(e ports.RuleUpdateRejected) { rig.events <- e },
		NewKeyID: func() string { return "key_restarted" }, Now: func() time.Time { return time.UnixMilli(rig.clock.Add(1)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	next.r = r
	return &next
}

func (rig *rulesRig) status(t *testing.T) domain.RulesStatus {
	t.Helper()
	st, err := rig.r.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// applies reports whether the active rule set flags the bundle of version v.
func applies(set domain.RuleSet, v int) bool {
	_, ok := domain.MatchDestructive(set.Destructive, fmt.Sprintf("v%d now", v))
	return ok
}
