package store

import "testing"

// TestMigration0006RuleState: the rule-update tables exist with their one state row, a bundle
// can carry its bytes, and the CHECKs refuse what they are there to refuse (delta
// `2026-10-signed-rule-bundles`, decision 1).
func TestMigration0006RuleState(t *testing.T) {
	t.Parallel()
	db := openTestStore(t).DB()
	ctx := t.Context()
	for kind, names := range map[string][]string{
		"table": {"rule_rejections", "rule_update_state", "trust_key_events"},
		"index": {"idx_trust_key_events_at"},
	} {
		for _, name := range names {
			var got string
			if err := db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = ? AND name = ?", kind, name).Scan(&got); err != nil {
				t.Errorf("migration 0006 did not create %s %s: %v", kind, name, err)
			}
		}
	}
	var failures, closed, ever int
	if err := db.QueryRowContext(ctx, `SELECT consecutive_failures, fail_closed, ever_had_valid_key FROM rule_update_state WHERE id = 1`).Scan(&failures, &closed, &ever); err != nil || failures != 0 || closed != 0 || ever != 0 {
		t.Errorf("the state row: %d %d %d %v, want one zeroed row", failures, closed, ever, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rule_bundles (version, sha256, source, installed_at, content) VALUES (1, 'x', 'builtin', 1, x'7b7d')`); err != nil {
		t.Errorf("a bundle with its bytes: %v", err)
	}
	digest := "0000000000000000000000000000000000000000000000000000000000000000"
	for name, stmt := range map[string]string{
		"a second state row":      `INSERT INTO rule_update_state (id) VALUES (2)`,
		"a negative count":        `UPDATE rule_update_state SET consecutive_failures = -1`,
		"a latch that is not 0/1": `UPDATE rule_update_state SET fail_closed = 2`,
		"an unknown reason":       `INSERT INTO rule_rejections (sha256, reason, at) VALUES ('` + digest + `', 'bored', 1)`,
		"a short digest":          `INSERT INTO rule_rejections (sha256, reason, at) VALUES ('abc', 'unsigned', 1)`,
		"an unknown key action":   `INSERT INTO trust_key_events (key_id, fingerprint, action, at) VALUES ('key_1', 'fp', 'rotated', 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rule_rejections (sha256, reason, version, at) VALUES (?, 'downgrade', 3, 1)`, digest); err != nil {
		t.Errorf("a valid rejection: %v", err)
	}
}
