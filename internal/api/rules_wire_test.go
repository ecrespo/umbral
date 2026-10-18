package api

import (
	"context"
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	secports "github.com/ecrespo/umbral/internal/security/ports"
)

// fakeRules answers fixed values and refuses with the manager's sentinels: what the test sees
// is api's half — the methods, the error mapping and the wire form.
type fakeRules struct {
	added []string
}

var fakeStatus = secdomain.RulesStatus{
	Active:        secdomain.BundleInfo{Version: 7, SHA256: "a7", Source: "remote", VerifiedWith: "key_1"},
	Previous:      &secdomain.BundleInfo{Version: 6, SHA256: "a6", Source: "remote", VerifiedWith: "key_1"},
	LocalOverride: true, RemoteUpdates: "enabled",
	TrustKeys:     []secdomain.KeyInfo{{ID: "key_1", Fingerprint: "SHA256:f1", Source: "builtin", AddedAt: 1}},
	LastRejection: "unknown_key", LastRejectionAt: 9,
}

func (f *fakeRules) Status(context.Context) (secdomain.RulesStatus, error) { return fakeStatus, nil }

func (f *fakeRules) ListKeys(context.Context) ([]secdomain.KeyInfo, error) {
	return fakeStatus.TrustKeys, nil
}

func (f *fakeRules) AddKey(_ context.Context, pub, confirm string) (secdomain.KeyInfo, error) {
	switch {
	case pub == "bad":
		return secdomain.KeyInfo{}, secdomain.ErrInvalidPublicKey
	case confirm != "SHA256:f2":
		return secdomain.KeyInfo{}, secdomain.ErrFingerprintMismatch
	case pub == "revoked":
		return secdomain.KeyInfo{}, secdomain.ErrKeyRevokedBySeed
	}
	f.added = append(f.added, pub)
	return secdomain.KeyInfo{ID: "key_2", Fingerprint: "SHA256:f2", Source: "user", AddedAt: 2}, nil
}

func (f *fakeRules) RemoveKey(_ context.Context, fp string, force bool) (secdomain.KeyInfo, secdomain.BundleInfo, error) {
	switch {
	case fp != "SHA256:f1":
		return secdomain.KeyInfo{}, secdomain.BundleInfo{}, secdomain.ErrUnknownKey
	case !force:
		return secdomain.KeyInfo{}, secdomain.BundleInfo{}, secdomain.ErrLastValidKey
	}
	at := int64(3)
	return secdomain.KeyInfo{ID: "key_1", Fingerprint: fp, Source: "builtin", AddedAt: 1, RevokedAt: &at},
		secdomain.BundleInfo{Version: 0, SHA256: "b0", Source: "builtin"}, nil
}

func (f *fakeRules) RotateKey(_ context.Context, _, _, confirm string) (secdomain.KeyInfo, secdomain.KeyInfo, secdomain.BundleInfo, error) {
	if confirm != "SHA256:f2" {
		return secdomain.KeyInfo{}, secdomain.KeyInfo{}, secdomain.BundleInfo{}, secdomain.ErrFingerprintMismatch
	}
	revoked := int64(4)
	return secdomain.KeyInfo{ID: "key_2", Fingerprint: "SHA256:f2", Source: "user", AddedAt: 4},
		secdomain.KeyInfo{ID: "key_1", Fingerprint: "SHA256:f1", Source: "builtin", AddedAt: 1, RevokedAt: &revoked},
		secdomain.BundleInfo{Source: "builtin", SHA256: "b0"}, nil
}

func (f *fakeRules) Rollback(context.Context) (secdomain.RulesStatus, error) {
	return secdomain.RulesStatus{}, secdomain.ErrNoPreviousBundle
}

func (f *fakeRules) Reset(context.Context) (secdomain.RulesStatus, error) {
	return secdomain.RulesStatus{Active: secdomain.BundleInfo{Source: "builtin", SHA256: "b0"}, RemoteUpdates: "disabled_fail_closed"}, nil
}

// TestRulesOverTheWire_REQ_SEC_014: `umb` may call rules.*, rules.status answers API §5.34's
// shape, the trust store's refusals map onto §3, and rules.update_rejected reaches clients.
func TestRulesOverTheWire_REQ_SEC_014(t *testing.T) {
	t.Parallel()
	svc := &fakeRules{}
	b := bus.New()
	s := testServerWithConfig(t, Config{Rules: svc, Bus: b})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Notify(ctx)
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("hello: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), "rules") {
		t.Errorf("rules is not advertised: %v", capabilitiesOf(t, hello.Result))
	}

	resp := c.call(2, "rules.status", nil)
	if resp.Error != nil {
		t.Fatalf("rules.status: %+v", resp.Error)
	}
	want := `{"active_bundle":{"version":7,"sha256":"a7","source":"remote","verified_with":"key_1"},` +
		`"previous_bundle":{"version":6,"sha256":"a6"},"local_override":true,"remote_updates":"enabled",` +
		`"trust_keys":[{"id":"key_1","fingerprint":"SHA256:f1","source":"builtin","added_at":1,"revoked_at":null}],` +
		`"last_rejection":{"reason":"unknown_key","at":9}}`
	if got := string(ordered[rulesStatusResult](t, resp.Result)); got != want {
		t.Errorf("status =\n%s\nwant\n%s", got, want)
	}
	resp = c.call(3, "rules.reset", nil)
	if got := string(ordered[rulesStatusResult](t, resp.Result)); resp.Error != nil ||
		got != `{"active_bundle":{"version":0,"sha256":"b0","source":"builtin","verified_with":null},"previous_bundle":null,"local_override":false,"remote_updates":"disabled_fail_closed","trust_keys":[],"last_rejection":null}` {
		t.Errorf("reset = %s %+v", got, resp.Error)
	}

	resp = c.call(4, "rules.key.add", map[string]any{"public_key": "good", "confirm_fingerprint": "SHA256:f2"})
	if resp.Error != nil || len(svc.added) != 1 {
		t.Fatalf("add: %+v %v", resp.Error, svc.added)
	}
	resp = c.call(5, "rules.key.remove", map[string]any{"fingerprint": "SHA256:f1", "force": true})
	if got := string(ordered[rulesKeyRemoveResult](t, resp.Result)); resp.Error != nil ||
		got != `{"key":{"id":"key_1","fingerprint":"SHA256:f1","source":"builtin","added_at":1,"revoked_at":3},"active_bundle":{"version":0,"sha256":"b0","source":"builtin","verified_with":null}}` {
		t.Errorf("remove = %s %+v", got, resp.Error)
	}

	resp = c.call(6, "rules.key.list", nil)
	if got := string(ordered[rulesKeyListResult](t, resp.Result)); resp.Error != nil ||
		got != `{"trust_keys":[{"id":"key_1","fingerprint":"SHA256:f1","source":"builtin","added_at":1,"revoked_at":null}]}` {
		t.Errorf("list = %s %+v", got, resp.Error)
	}
	resp = c.call(7, "rules.key.rotate", map[string]any{"fingerprint": "SHA256:f1", "public_key": "k", "confirm_fingerprint": "SHA256:f2"})
	if got := string(ordered[rulesKeyRotateResult](t, resp.Result)); resp.Error != nil ||
		got != `{"added":{"id":"key_2","fingerprint":"SHA256:f2","source":"user","added_at":4,"revoked_at":null},`+
			`"revoked":{"id":"key_1","fingerprint":"SHA256:f1","source":"builtin","added_at":1,"revoked_at":4},`+
			`"active_bundle":{"version":0,"sha256":"b0","source":"builtin","verified_with":null}}` {
		t.Errorf("rotate = %s %+v", got, resp.Error)
	}

	for name, tc := range map[string]struct {
		method string
		params map[string]any
		code   string
	}{
		"a malformed key":         {"rules.key.add", map[string]any{"public_key": "bad", "confirm_fingerprint": "x"}, domainValidationError},
		"a wrong fingerprint":     {"rules.key.add", map[string]any{"public_key": "good", "confirm_fingerprint": "x"}, domainValidationError},
		"a seed-revoked key":      {"rules.key.add", map[string]any{"public_key": "revoked", "confirm_fingerprint": "SHA256:f2"}, domainConflict},
		"an unknown key":          {"rules.key.remove", map[string]any{"fingerprint": "SHA256:nope"}, domainNotFound},
		"the last key, no force":  {"rules.key.remove", map[string]any{"fingerprint": "SHA256:f1"}, domainConflict},
		"a rotate unconfirmed":    {"rules.key.rotate", map[string]any{"fingerprint": "SHA256:f1", "public_key": "k", "confirm_fingerprint": "x"}, domainValidationError},
		"nothing to roll back to": {"rules.rollback", nil, domainConflict},
	} {
		resp := c.call(6, tc.method, tc.params)
		if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != tc.code {
			t.Errorf("%s: %+v, want %s", name, resp.Error, tc.code)
		}
	}

	b.Publish(secports.RuleUpdateRejected{Reason: secdomain.RejectDowngrade, Version: 5, Source: "remote"})
	method, _, n := c.readNotification(t)
	if method != "rules.update_rejected" || string(n) != `{"reason":"downgrade","version":5,"source":"remote"}` {
		t.Fatalf("rules.update_rejected = %s %s", method, n)
	}
}

// TestRulesAreNotImplementedWithoutTheModule: a daemon built without the rule manager answers
// NOT_IMPLEMENTED and does not advertise `rules`.
func TestRulesAreNotImplementedWithoutTheModule(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	if hello := c.hello(s.Token(), ClientCLI); slices.Contains(capabilitiesOf(t, hello.Result), "rules") {
		t.Error("rules is advertised without the module")
	}
	if resp := c.call(2, "rules.status", nil); resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("rules.status = %+v", resp.Error)
	}
}
