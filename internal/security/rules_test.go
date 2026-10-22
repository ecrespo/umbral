package security

import (
	"errors"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/security/domain"
)

func seedOf(keys ...signer) domain.Seed {
	var s domain.Seed
	for i, k := range keys {
		s.Keys = append(s.Keys, domain.SeedKey{ID: "key_seed" + string(rune('a'+i)), PublicKey: k.pub})
	}
	return s
}

// TestRejectsUnknownKeyAndDowngrade_REQ_SEC_013: a bundle signed by an unknown key, or not
// newer than the installed one, is discarded with its reason recorded — in the update state,
// in egress_log and to clients — the rules in force stay, and the same bundle is not retried.
func TestRejectsUnknownKeyAndDowngrade_REQ_SEC_013(t *testing.T) {
	t.Parallel()
	project, stranger := newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	ctx := t.Context()

	rig.r.Offer(ctx, bundleOf(5), project.sign(t, bundleOf(5)))
	if st := rig.status(t); st.Active.Version != 5 || !applies(rig.base.last(), 5) {
		t.Fatalf("a verified bundle was not applied: %+v", st.Active)
	}

	for _, tc := range []struct {
		name   string
		bundle []byte
		sig    []byte
		reason domain.RejectReason
	}{
		{"an unknown key", bundleOf(6), stranger.sign(t, bundleOf(6)), domain.RejectUnknownKey},
		{"the same version", []byte(`{"version": 5}`), project.sign(t, []byte(`{"version": 5}`)), domain.RejectDowngrade},
		{"an older version", bundleOf(4), project.sign(t, bundleOf(4)), domain.RejectDowngrade},
	} {
		rig.r.Offer(ctx, tc.bundle, tc.sig)
		select {
		case ev := <-rig.events:
			if ev.Reason != tc.reason || ev.Source != domain.SourceRemote {
				t.Errorf("%s: announced %+v, want %s", tc.name, ev, tc.reason)
			}
		default:
			t.Errorf("%s: no rules.update_rejected", tc.name)
		}
		st := rig.status(t)
		if st.Active.Version != 5 || st.LastRejection != tc.reason || !applies(rig.base.last(), 5) {
			t.Errorf("%s: active %d, last rejection %q; want the rules in force kept and %s recorded", tc.name, st.Active.Version, st.LastRejection, tc.reason)
		}
	}
	d := rig.store.snapshot()
	if len(d.egress) != 3 || d.egress[0].Provider != RejectedEgressProvider+"unknown_key" || d.egress[0].Host != "rules.example" || d.egress[0].PayloadSHA256 != domain.BundleDigest(bundleOf(6)) {
		t.Errorf("egress rows %+v, want one per rejection naming its reason", d.egress)
	}
	if len(rig.metrics.got) != 3 {
		t.Errorf("metrics %v", rig.metrics.got)
	}

	// Not retried: the same bundle again changes nothing and announces nothing.
	rig.r.Offer(ctx, bundleOf(6), stranger.sign(t, bundleOf(6)))
	if d := rig.store.snapshot(); len(d.egress) != 3 || d.state.ConsecutiveFailures != 3 && !d.state.FailClosed {
		t.Errorf("a rejected bundle was retried: %d egress rows, state %+v", len(d.egress), d.state)
	}
	select {
	case ev := <-rig.events:
		t.Errorf("a repeat was announced: %+v", ev)
	default:
	}
}

// TestAnUnchangedBundleIsUpToDate_REQ_SEC_013: a fetched bundle identical to the active one is
// not a rejection — nothing is recorded, announced or counted (DD-016).
func TestAnUnchangedBundleIsUpToDate_REQ_SEC_013(t *testing.T) {
	t.Parallel()
	project := newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	sig := project.sign(t, bundleOf(2))
	rig.r.Offer(t.Context(), bundleOf(2), sig)
	for range 5 {
		rig.r.Offer(t.Context(), bundleOf(2), sig)
	}
	d := rig.store.snapshot()
	if len(d.egress) != 0 || d.state.ConsecutiveFailures != 0 || d.state.FailClosed || len(rig.events) != 0 {
		t.Errorf("an unchanged bundle counted: %d egress rows, state %+v, %d events", len(d.egress), d.state, len(rig.events))
	}
}

// TestKeyLifecycleRequiresFingerprint_REQ_SEC_014: adding or rotating needs the key's own
// fingerprint confirmed, removing the last valid key needs force, and every change is recorded
// with its timestamp.
func TestKeyLifecycleRequiresFingerprint_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	a, b, c := newSigner(t), newSigner(t), newSigner(t)
	rig := newRulesRig(t, domain.Seed{})
	ctx := t.Context()

	if _, err := rig.r.AddKey(ctx, a.b64(), b.fp()); !errors.Is(err, domain.ErrFingerprintMismatch) {
		t.Errorf("add with another key's fingerprint: %v", err)
	}
	if _, err := rig.r.AddKey(ctx, a.b64(), ""); !errors.Is(err, domain.ErrFingerprintMismatch) {
		t.Errorf("add without confirmation: %v", err)
	}
	if _, err := rig.r.AddKey(ctx, "not-base64", a.fp()); !errors.Is(err, domain.ErrInvalidPublicKey) {
		t.Errorf("add of a malformed key: %v", err)
	}
	if n := len(rig.store.snapshot().keys); n != 0 {
		t.Fatalf("a refused add wrote %d keys", n)
	}
	added, err := rig.r.AddKey(ctx, a.b64(), a.fp())
	if err != nil || added.Fingerprint != a.fp() || added.Source != domain.SourceUser || added.AddedAt == 0 || !strings.HasPrefix(added.ID, "key_") {
		t.Fatalf("add: %+v %v", added, err)
	}
	if again, err := rig.r.AddKey(ctx, a.b64(), a.fp()); err != nil || again.ID != added.ID {
		t.Errorf("re-adding a valid key: %+v %v, want the same key unchanged", again, err)
	}

	if _, _, _, err := rig.r.RotateKey(ctx, a.fp(), b.b64(), c.fp()); !errors.Is(err, domain.ErrFingerprintMismatch) {
		t.Errorf("rotate with the wrong fingerprint: %v", err)
	}
	if _, _, _, err := rig.r.RotateKey(ctx, c.fp(), b.b64(), b.fp()); !errors.Is(err, domain.ErrUnknownKey) {
		t.Errorf("rotate of an unknown key: %v", err)
	}
	newKey, old, _, err := rig.r.RotateKey(ctx, a.fp(), b.b64(), b.fp())
	if err != nil || newKey.Fingerprint != b.fp() || old.RevokedAt == nil {
		t.Fatalf("rotate: %+v %+v %v", newKey, old, err)
	}

	if _, _, err := rig.r.RemoveKey(ctx, b.fp(), false); !errors.Is(err, domain.ErrLastValidKey) {
		t.Errorf("removing the last valid key without force: %v", err)
	}
	if _, _, err := rig.r.RemoveKey(ctx, c.fp(), true); !errors.Is(err, domain.ErrUnknownKey) {
		t.Errorf("removing an unknown key: %v", err)
	}
	if removed, _, err := rig.r.RemoveKey(ctx, b.fp(), true); err != nil || removed.RevokedAt == nil {
		t.Errorf("removing with force: %+v %v", removed, err)
	}

	events := rig.store.snapshot().events
	actions := make([]string, 0, len(events))
	for _, e := range events {
		if e.At == 0 {
			t.Errorf("an event without its timestamp: %+v", e)
		}
		actions = append(actions, e.Action)
	}
	want := []string{domain.KeyAdded, domain.KeyUnchanged, domain.KeyAdded, domain.KeyRevoked, domain.KeyRevoked}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("recorded %v, want %v", actions, want)
	}
}

// TestTrustSeedMatchesByFingerprint_REQ_SEC_014: the seed is matched by fingerprint, so a key
// the store already holds is never inserted twice, a seed key the user removed stays removed,
// a key the seed revokes is revoked and cannot be added, and every fingerprint the seed knows
// reports `builtin`.
func TestTrustSeedMatchesByFingerprint_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	project, retired, compromised := newSigner(t), newSigner(t), newSigner(t)
	rig := newRulesRig(t, domain.Seed{})
	ctx := t.Context()
	// The user trusted two keys before a build carried them.
	if _, err := rig.r.AddKey(ctx, project.b64(), project.fp()); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.r.AddKey(ctx, compromised.b64(), compromised.fp()); err != nil {
		t.Fatal(err)
	}
	seed := domain.Seed{Keys: []domain.SeedKey{{ID: "key_seed", PublicKey: project.pub}}, Retired: []string{retired.fp()}, Revoked: []string{compromised.fp()}}
	rig = rig.restart(t, seed)

	keys := rig.status(t).TrustKeys
	if len(keys) != 2 {
		t.Fatalf("%d keys, want the two the store had: the seed key matched by fingerprint, not inserted again", len(keys))
	}
	for _, k := range keys {
		if k.Source != domain.SourceBuiltin {
			t.Errorf("%s reports %s, want builtin for a fingerprint the seed knows", k.Fingerprint, k.Source)
		}
		if (k.Fingerprint == compromised.fp()) != (k.RevokedAt != nil) {
			t.Errorf("%s revoked=%v", k.Fingerprint, k.RevokedAt != nil)
		}
	}
	if _, err := rig.r.AddKey(ctx, compromised.b64(), compromised.fp()); !errors.Is(err, domain.ErrKeyRevokedBySeed) {
		t.Errorf("adding a seed-revoked key: %v", err)
	}
	if _, err := rig.r.AddKey(ctx, retired.b64(), retired.fp()); err != nil {
		t.Fatal(err)
	}

	// A seed key the user removed is not brought back by the next start.
	if _, _, err := rig.r.RemoveKey(ctx, project.fp(), false); err != nil {
		t.Fatal(err)
	}
	rig = rig.restart(t, seed)
	for _, k := range rig.status(t).TrustKeys {
		if k.Fingerprint == project.fp() && k.RevokedAt == nil {
			t.Error("the seed brought back a key the user removed")
		}
		if k.Fingerprint == retired.fp() && k.Source != domain.SourceBuiltin {
			t.Error("a retired fingerprint does not report builtin")
		}
	}
	// A fresh store takes the seed key with the id the seed fixes.
	fresh := newRulesRig(t, seed)
	if keys := fresh.status(t).TrustKeys; len(keys) != 1 || keys[0].ID != "key_seed" {
		t.Errorf("a fresh store: %+v, want the seed key with its fixed id", keys)
	}
}

// TestRemoveRevokesAndDiscardsBundles_REQ_SEC_014: removing a key revokes it, discards the
// bundles it verified — the active one included, the newest remaining remote bundle or the
// built-in rules taking over — and forgets downgrade rejections.
func TestRemoveRevokesAndDiscardsBundles_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	a, b := newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(a, b))
	ctx := t.Context()
	rig.r.Offer(ctx, bundleOf(1), a.sign(t, bundleOf(1)))
	rig.r.Offer(ctx, bundleOf(2), b.sign(t, bundleOf(2)))
	rig.r.Offer(ctx, bundleOf(1), a.sign(t, bundleOf(1))) // a downgrade, remembered
	if st := rig.status(t); st.Active.Version != 2 || st.Previous == nil || st.Previous.Version != 1 || st.LastRejection != domain.RejectDowngrade {
		t.Fatalf("setup: %+v", st)
	}

	_, active, err := rig.r.RemoveKey(ctx, b.fp(), false)
	if err != nil || active.Version != 1 {
		t.Fatalf("removing the key that verified the active bundle: active %+v %v, want the previous one", active, err)
	}
	if !applies(rig.base.last(), 1) || applies(rig.base.last(), 2) {
		t.Error("the rules in force were not switched to the surviving bundle")
	}
	d := rig.store.snapshot()
	if _, ok := d.bundles[2]; ok {
		t.Error("the bundle the revoked key verified is still stored")
	}
	if len(d.rejections) != 0 {
		t.Errorf("downgrade rejections were not forgotten: %+v", d.rejections)
	}

	if _, active, err = rig.r.RemoveKey(ctx, a.fp(), true); err != nil || active.Source != domain.SourceBuiltin || active.Version != 0 {
		t.Errorf("removing the last key: active %+v %v, want the built-in rules", active, err)
	}
	if applies(rig.base.last(), 1) {
		t.Error("the built-in rules are not in force")
	}
	if len(rig.status(t).TrustKeys) != 2 {
		t.Error("a removed key was deleted rather than revoked")
	}
}

// TestFailClosedAfterThreeFailures_REQ_SEC_015: three consecutive rejections disable remote
// updates, the rules in force stay, and a fourth bundle — even a valid one — is not applied.
func TestFailClosedAfterThreeFailures_REQ_SEC_015(t *testing.T) {
	t.Parallel()
	project, stranger := newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	ctx := t.Context()
	rig.r.Offer(ctx, bundleOf(1), project.sign(t, bundleOf(1)))
	// Two failures, then a success: the count starts again.
	rig.r.Offer(ctx, bundleOf(2), stranger.sign(t, bundleOf(2)))
	rig.r.Offer(ctx, bundleOf(3), nil)
	rig.r.Offer(ctx, bundleOf(4), project.sign(t, bundleOf(4)))
	if st := rig.status(t); st.RemoteUpdates != domain.UpdatesEnabled || st.Active.Version != 4 {
		t.Fatalf("a success did not reset the count: %+v", st)
	}
	for v := 5; v <= 6; v++ {
		rig.r.Offer(ctx, bundleOf(v), stranger.sign(t, bundleOf(v)))
	}
	if st := rig.status(t); st.RemoteUpdates != domain.UpdatesEnabled {
		t.Fatalf("two failures after a success failed closed: the success did not reset the count")
	}
	rig.r.Offer(ctx, bundleOf(7), stranger.sign(t, bundleOf(7)))
	if st := rig.status(t); st.RemoteUpdates != domain.UpdatesFailClosed || st.Active.Version != 4 {
		t.Fatalf("after three failures: %s, active %d", st.RemoteUpdates, st.Active.Version)
	}
	if rig.r.UpdatesAllowed(ctx) {
		t.Error("the fetcher would still fetch")
	}
	rig.r.Offer(ctx, bundleOf(8), project.sign(t, bundleOf(8)))
	if st := rig.status(t); st.Active.Version != 4 || !applies(rig.base.last(), 4) {
		t.Errorf("a valid bundle was applied while failed closed: active %d", st.Active.Version)
	}
	if rig = rig.restart(t, seedOf(project)); rig.status(t).RemoteUpdates != domain.UpdatesFailClosed {
		t.Error("a restart reopened updates")
	}
}

// TestSeedDoesNotReopenFailClosed_REQ_SEC_015: a newer binary's seed adds its keys but does
// not re-enable updates that failed closed; only a manual add does.
func TestSeedDoesNotReopenFailClosed_REQ_SEC_015(t *testing.T) {
	t.Parallel()
	old, stranger, next := newSigner(t), newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(old))
	for v := 1; v <= 3; v++ {
		rig.r.Offer(t.Context(), bundleOf(v), stranger.sign(t, bundleOf(v)))
	}
	rig = rig.restart(t, domain.Seed{Keys: []domain.SeedKey{{ID: "key_old", PublicKey: old.pub}, {ID: "key_next", PublicKey: next.pub}}})
	st := rig.status(t)
	if st.RemoteUpdates != domain.UpdatesFailClosed {
		t.Errorf("the new seed reopened updates: %s", st.RemoteUpdates)
	}
	if len(st.TrustKeys) != 2 {
		t.Errorf("the new seed key was not added: %+v", st.TrustKeys)
	}

	// The store ending up with no valid key fails closed too, and stays so after a restart.
	lone := newSigner(t)
	r2 := newRulesRig(t, seedOf(lone))
	if _, _, err := r2.r.RemoveKey(t.Context(), lone.fp(), true); err != nil {
		t.Fatal(err)
	}
	if r2 = r2.restart(t, seedOf(lone, newSigner(t))); r2.status(t).RemoteUpdates != domain.UpdatesFailClosed {
		t.Error("after the last key was removed, a seed key reopened updates")
	}
	// A store that has never held a valid key is not latched: the first seed key opens it.
	empty := newRulesRig(t, domain.Seed{})
	if empty.status(t).RemoteUpdates != domain.UpdatesFailClosed {
		t.Error("with no key at all, updates are not reported closed")
	}
	if empty = empty.restart(t, seedOf(newSigner(t))); empty.status(t).RemoteUpdates != domain.UpdatesEnabled {
		t.Error("a fresh install did not pick up its first seed key")
	}
}

// TestAddOfSeededKeyReopensFailClosed_REQ_SEC_015: a manual add reopens updates even for a
// key the seed already inserted — a recorded no-op on the trust store.
func TestAddOfSeededKeyReopensFailClosed_REQ_SEC_015(t *testing.T) {
	t.Parallel()
	project, stranger := newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	for v := 1; v <= 3; v++ {
		rig.r.Offer(t.Context(), bundleOf(v), stranger.sign(t, bundleOf(v)))
	}
	before := len(rig.store.snapshot().events)
	k, err := rig.r.AddKey(t.Context(), project.b64(), project.fp())
	if err != nil || k.ID != "key_seeda" {
		t.Fatalf("add of the seeded key: %+v %v", k, err)
	}
	d := rig.store.snapshot()
	if st := rig.status(t); st.RemoteUpdates != domain.UpdatesEnabled {
		t.Errorf("updates stay %s", st.RemoteUpdates)
	}
	if len(d.keys) != 1 || len(d.events) != before+1 || d.events[len(d.events)-1].Action != domain.KeyUnchanged {
		t.Errorf("keys %d, events %+v; want no new key and one recorded no-op", len(d.keys), d.events[before:])
	}
	rig.r.Offer(t.Context(), bundleOf(9), project.sign(t, bundleOf(9)))
	if rig.status(t).Active.Version != 9 {
		t.Error("a valid bundle after the add was not applied")
	}
}

// TestRollbackAndResetWorkOffline_REQ_SEC_016: rollback reinstalls the previous bundle and
// reset the built-in rules, with no key and no network, the local layer untouched (the base is
// all they replace), and neither is undone by the next check.
func TestRollbackAndResetWorkOffline_REQ_SEC_016(t *testing.T) {
	t.Parallel()
	project := newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	ctx := t.Context()
	if _, err := rig.r.Rollback(ctx); !errors.Is(err, domain.ErrNoPreviousBundle) {
		t.Errorf("rollback with nothing to return to: %v", err)
	}
	rig.r.Offer(ctx, bundleOf(1), project.sign(t, bundleOf(1)))
	rig.r.Offer(ctx, bundleOf(2), project.sign(t, bundleOf(2)))
	// Offline and with no usable key: rules_check off and the store failed closed. The manager
	// has no network of its own, so nothing here can fetch.
	rig.r.cfg.Enabled = false
	rig.store.mu.Lock()
	rig.store.data.state.FailClosed = true
	rig.store.mu.Unlock()

	st, err := rig.r.Rollback(ctx)
	if err != nil || st.Active.Version != 1 || !applies(rig.base.last(), 1) {
		t.Fatalf("rollback: %+v %v", st.Active, err)
	}
	rig.r.cfg.Enabled = true
	if _, err := rig.r.AddKey(ctx, project.b64(), project.fp()); err != nil { // reopen
		t.Fatal(err)
	}
	rig.r.Offer(ctx, bundleOf(2), project.sign(t, bundleOf(2)))
	if rig.status(t).Active.Version != 1 {
		t.Error("the next check reinstalled the bundle rolled back from")
	}
	rig.r.Offer(ctx, bundleOf(3), project.sign(t, bundleOf(3)))
	if rig.status(t).Active.Version != 3 {
		t.Error("a newer bundle after a rollback was not applied")
	}

	st, err = rig.r.Reset(ctx)
	if err != nil || st.Active.Source != domain.SourceBuiltin || st.Active.Version != 0 || applies(rig.base.last(), 3) {
		t.Fatalf("reset: %+v %v", st.Active, err)
	}
	rig.r.Offer(ctx, bundleOf(3), project.sign(t, bundleOf(3)))
	if rig.status(t).Active.Version != 0 {
		t.Error("the next check undid the reset")
	}
	if st, err = rig.r.Rollback(ctx); err != nil || st.Active.Version != 3 {
		t.Errorf("rollback after a reset: %+v %v, want the newest remote bundle back", st.Active, err)
	}
	if len(rig.store.snapshot().egress) != 0 || len(rig.events) != 0 {
		t.Error("a rollback or reset was recorded as a rejection")
	}
}

// TestAResetKeepsTheVersionFloor_REQ_SEC_016: a reset makes the built-in rules (version 0)
// active, but the floor a new bundle must exceed is the highest version ever applied, so a
// compromised endpoint cannot replay an older signed bundle to undo the reset. Only a
// revocation lowers the floor, to what its surviving bundles hold (DD-016).
func TestAResetKeepsTheVersionFloor_REQ_SEC_016(t *testing.T) {
	t.Parallel()
	project, other := newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	ctx := t.Context()
	if _, err := rig.r.AddKey(ctx, other.b64(), other.fp()); err != nil {
		t.Fatal(err)
	}
	for v := 1; v <= 3; v++ {
		rig.r.Offer(ctx, bundleOf(v), project.sign(t, bundleOf(v)))
	}
	if _, err := rig.r.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	rig.r.Offer(ctx, bundleOf(2), project.sign(t, bundleOf(2)))
	if st := rig.status(t); st.Active.Version != 0 || st.LastRejection != domain.RejectDowngrade {
		t.Fatalf("an older signed bundle after a reset: active %+v, last rejection %+v", st.Active, st.LastRejection)
	}
	rig.r.Offer(ctx, bundleOf(4), project.sign(t, bundleOf(4)))
	if rig.status(t).Active.Version != 4 {
		t.Fatal("a newer bundle after a reset was not applied")
	}
	// Every bundle the project key verified goes with it, and so does the floor they set.
	if _, _, err := rig.r.RemoveKey(ctx, project.fp(), false); err != nil {
		t.Fatal(err)
	}
	rig.r.Offer(ctx, bundleOf(2), other.sign(t, bundleOf(2)))
	if rig.status(t).Active.Version != 2 {
		t.Errorf("after revoking the key that signed version 4, version 2 of another key: active %+v", rig.status(t).Active)
	}
}

// TestASeedRevocationLowersTheFloor_REQ_SEC_014: a key the seed revokes at start loses its
// bundles and the floor they set, like any revocation; a later state write in Start must not
// put the old floor back, or a stolen key's huge version would lock legitimate bundles out.
func TestASeedRevocationLowersTheFloor_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	project, other := newSigner(t), newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	ctx := t.Context()
	if _, err := rig.r.AddKey(ctx, other.b64(), other.fp()); err != nil {
		t.Fatal(err)
	}
	rig.r.Offer(ctx, bundleOf(9), project.sign(t, bundleOf(9)))
	if rig.status(t).Active.Version != 9 {
		t.Fatal("version 9 was not applied")
	}
	compromised := seedOf(project)
	compromised.Revoked = []string{project.fp()}
	rig = rig.restart(t, compromised)
	rig.r.Offer(ctx, bundleOf(2), other.sign(t, bundleOf(2)))
	if st := rig.status(t); st.Active.Version != 2 {
		t.Errorf("after the seed revoked the key that signed version 9, version 2 of another key: active %+v, last rejection %q", st.Active, st.LastRejection)
	}
}

// TestAStoredBundleIsCheckedAtStart_REQ_SEC_011: at start the active bundle's bytes must match
// its digest; a row that does not is not trusted and the built-in rules apply.
func TestAStoredBundleIsCheckedAtStart_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	project := newSigner(t)
	rig := newRulesRig(t, seedOf(project))
	rig.r.Offer(t.Context(), bundleOf(1), project.sign(t, bundleOf(1)))
	if next := rig.restart(t, seedOf(project)); !applies(next.base.last(), 1) {
		t.Fatal("a restart did not reload the active bundle")
	}
	rig.store.mu.Lock()
	b := rig.store.data.bundles[1]
	b.Content = bundleOf(9)
	rig.store.data.bundles[1] = b
	rig.store.mu.Unlock()
	if next := rig.restart(t, seedOf(project)); applies(next.base.last(), 9) || applies(next.base.last(), 1) {
		t.Error("a stored bundle whose bytes changed was trusted")
	}
}
