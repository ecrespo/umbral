package store

import "testing"

// TestALeftWorktreeClearsFullAuto_REQ_ORC_004: a worktree that leaves `active` — merged,
// discarded or missing — clears `full_auto` on the threads it was the worktree of, in the same
// write, and leaves `mode` as it was; a worktree deleted under its thread clears it too. A
// thread whose worktree stays active keeps it.
func TestALeftWorktreeClearsFullAuto_REQ_ORC_004(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := t.Context()
	seed := `INSERT INTO threads(id, mode, cwd, created_at, updated_at) VALUES
		('thr_m', 'auto-edit', '/r', 1, 1), ('thr_d', 'ask', '/r', 1, 1), ('thr_x', 'normal', '/r', 1, 1),
		('thr_g', 'normal', '/r', 1, 1), ('thr_k', 'normal', '/r', 1, 1);
	INSERT INTO worktrees(id, thread_id, repo_root, repo_id, base_commit, origin_branch, branch, path, created_at, updated_at) VALUES
		('wtr_m', 'thr_m', '/r', 'id', 'c', 'main', 'umbral/thr_m', '/w/m', 1, 1),
		('wtr_d', 'thr_d', '/r', 'id', 'c', 'main', 'umbral/thr_d', '/w/d', 1, 1),
		('wtr_x', 'thr_x', '/r', 'id', 'c', 'main', 'umbral/thr_x', '/w/x', 1, 1),
		('wtr_g', 'thr_g', '/r', 'id', 'c', 'main', 'umbral/thr_g', '/w/g', 1, 1),
		('wtr_k', 'thr_k', '/r', 'id', 'c', 'main', 'umbral/thr_k', '/w/k', 1, 1);
	UPDATE threads SET worktree_id = 'wtr_' || substr(id, 5), full_auto = 1;`
	if _, err := s.DB().ExecContext(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE worktrees SET state = 'merged' WHERE id = 'wtr_m'`,
		`UPDATE worktrees SET state = 'discarded' WHERE id = 'wtr_d'`,
		`UPDATE worktrees SET state = 'missing' WHERE id = 'wtr_x'`,
		`UPDATE threads SET worktree_id = NULL WHERE id = 'thr_g'`,
		`UPDATE worktrees SET updated_at = 2 WHERE id = 'wtr_k'`,
	} {
		if _, err := s.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for id, want := range map[string]struct {
		mode     string
		fullAuto int
	}{
		"thr_m": {"auto-edit", 0}, "thr_d": {"ask", 0}, "thr_x": {"normal", 0},
		"thr_g": {"normal", 0}, "thr_k": {"normal", 1},
	} {
		var mode string
		var fullAuto int
		if err := s.DB().QueryRowContext(ctx, `SELECT mode, full_auto FROM threads WHERE id = ?`, id).Scan(&mode, &fullAuto); err != nil {
			t.Fatal(err)
		}
		if mode != want.mode || fullAuto != want.fullAuto {
			t.Errorf("%s: mode=%s full_auto=%d, want %s and %d", id, mode, fullAuto, want.mode, want.fullAuto)
		}
	}
}
