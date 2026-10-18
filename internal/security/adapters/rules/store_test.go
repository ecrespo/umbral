package rules

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// TestTheRuleStoreKeepsWhatTheManagerWrites_REQ_SEC_011: every RuleTx operation round-trips
// through SQLite, one transaction is all or nothing, one row is active at a time, and a remote
// row without its key or its bytes is refused (Data Model §2.4e).
func TestTheRuleStoreKeepsWhatTheManagerWrites_REQ_SEC_011(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewStore(db)
	at := int64(7)
	digest := domain.BundleDigest([]byte(`{"version": 1}`))

	err = s.Update(ctx, func(tx ports.RuleTx) error {
		for _, step := range []func() error{
			func() error {
				return tx.InsertKey(domain.TrustKey{ID: "key_a", PublicKey: make([]byte, 32), Fingerprint: "SHA256:a", AddedAt: 1})
			},
			func() error { return tx.SetRevoked("key_a", &at) },
			func() error {
				return tx.KeyEvent(domain.KeyEvent{KeyID: "key_a", Fingerprint: "SHA256:a", Action: domain.KeyRevoked, At: at})
			},
			func() error {
				return tx.PutBundle(domain.BundleRecord{Version: 0, SHA256: "b", Source: domain.SourceBuiltin, InstalledAt: 1, Active: true})
			},
			func() error {
				return tx.PutBundle(domain.BundleRecord{Version: 1, SHA256: digest, Source: domain.SourceRemote, VerifiedWith: "key_a", InstalledAt: 2, Content: []byte(`{"version": 1}`)})
			},
			func() error { return tx.SetActive(1) },
			func() error {
				return tx.PutRejection(domain.Rejection{SHA256: digest, Reason: domain.RejectDowngrade, Version: 1, At: at})
			},
			func() error {
				return tx.SetState(domain.UpdateState{ConsecutiveFailures: 2, FailClosed: true, EverHadValidKey: true, LastRejection: domain.RejectUnknownKey, LastRejectionAt: at})
			},
			func() error {
				return tx.Egress(llmdomain.EgressRecord{Provider: "umbral-rules:rejected:unknown_key", Host: "h", Bytes: 3, PayloadSHA256: digest, CreatedAt: at})
			},
		} {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Update(ctx, func(tx ports.RuleTx) error {
		keys, err := tx.Keys()
		if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil || *keys[0].RevokedAt != at || len(keys[0].PublicKey) != 32 {
			t.Errorf("keys %+v %v", keys, err)
		}
		bundles, err := tx.Bundles()
		if err != nil || len(bundles) != 2 || !bundles[0].Active || bundles[0].Version != 1 || bundles[1].Active || string(bundles[0].Content) != `{"version": 1}` || bundles[0].VerifiedWith != "key_a" {
			t.Errorf("bundles %+v %v", bundles, err)
		}
		if seen, err := tx.Rejection(digest); !seen || err != nil {
			t.Errorf("rejection %v %v", seen, err)
		}
		st, err := tx.State()
		if err != nil || st.ConsecutiveFailures != 2 || !st.FailClosed || !st.EverHadValidKey || st.LastRejection != domain.RejectUnknownKey || st.LastRejectionAt != at {
			t.Errorf("state %+v %v", st, err)
		}
		if err := tx.DeleteRejections(domain.RejectDowngrade); err != nil {
			return err
		}
		if seen, _ := tx.Rejection(digest); seen {
			t.Error("DeleteRejections left the downgrade")
		}
		if err := tx.PutBundle(domain.BundleRecord{Version: 2, SHA256: "x", Source: domain.SourceRemote, InstalledAt: 3}); err == nil {
			t.Error("a remote bundle without its key and bytes was written")
		}
		if err := tx.SetActive(9); err == nil {
			t.Error("activating a missing version succeeded")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// All or nothing: a failing transaction leaves no trace.
	boom := errors.New("boom")
	if err := s.Update(ctx, func(tx ports.RuleTx) error {
		if err := tx.InsertKey(domain.TrustKey{ID: "key_b", PublicKey: make([]byte, 32), Fingerprint: "SHA256:b", AddedAt: 2}); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("update: %v", err)
	}
	var n int
	if err := db.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM trust_keys`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d keys after a failed transaction, want 1", n)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT count(*) FROM egress_log WHERE provider = 'umbral-rules:rejected:unknown_key'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the rejection's egress row: %d %v", n, err)
	}
}
