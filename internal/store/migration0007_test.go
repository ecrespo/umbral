package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openAt returns the path of a database that stopped at migration `version`, as a daemon of
// that release left it, so a test can seed old rows and then let Open upgrade it.
func openAt(t *testing.T, version int, seed ...string) string {
	t.Helper()
	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "umbral.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	for _, m := range available {
		if m.version > version {
			break
		}
		if _, err := raw.ExecContext(t.Context(), m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
		if _, err := raw.ExecContext(t.Context(),
			"INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)", m.version); err != nil {
			t.Fatalf("record %s: %v", m.name, err)
		}
	}
	for _, stmt := range seed {
		if _, err := raw.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seed at %d: %q: %v", version, stmt, err)
		}
	}
	return path
}

// TestMigration0007KeepsF1Rows_REQ_ORC_004 is T-F2-01's Done: a database holding F1 rows — a
// thread with a message, a tool call, an approval and an agent block — upgrades past 0006
// without a table being rebuilt. Every row is still there, the new columns read their
// defaults (no thread is in full-auto, nothing queued, everything observed, every call and
// block Umbral's own), and `foreign_key_check` finds nothing.
func TestMigration0007KeepsF1Rows_REQ_ORC_004(t *testing.T) {
	t.Parallel()
	path := openAt(t, 6,
		`INSERT INTO threads(id, mode, cwd, created_at, updated_at) VALUES ('thr_old', 'auto-edit', '/repo', 1, 1)`,
		`INSERT INTO messages(id, thread_id, turn_id, role, content, created_at) VALUES ('msg_old', 'thr_old', 'trn_1', 'assistant', 'x', 1)`,
		`INSERT INTO sessions(id, shell, cwd, cols, rows, state, owner_thread_id, created_at) VALUES ('ses_old', '/bin/sh', '/repo', 80, 24, 'exited', 'thr_old', 1)`,
		`INSERT INTO blocks(id, session_id, origin, thread_id, command, state, started_at) VALUES ('blk_old', 'ses_old', 'agent', 'thr_old', 'go test', 'finished', 1)`,
		`INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, block_id, started_at) VALUES ('tc_old', 'thr_old', 'msg_old', 'run_command', 'Exec', '{}', 'ok', 'blk_old', 1)`,
		`INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, state, created_at) VALUES ('apr_old', 'thr_old', 'tc_old', 'run_command', 'Exec', 'policy', 'go test', 'approved', 1)`,
	)
	s, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("upgrade past 0006: %v", err)
	}
	defer func() { _ = s.Close() }()
	db := s.DB()
	ctx := t.Context()

	if v, err := s.SchemaVersion(ctx); err != nil || v < 7 {
		t.Fatalf("schema version %d (%v), want at least 7", v, err)
	}
	var mode, visibility string
	var fullAuto, queued int
	var worktree, workspace, agent sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT mode, full_auto, queued, worktree_id, workspace_id, agent, egress_visibility
		FROM threads WHERE id = 'thr_old'`).Scan(&mode, &fullAuto, &queued, &worktree, &workspace, &agent, &visibility); err != nil {
		t.Fatalf("read the thread's new columns: %v", err)
	}
	if mode != "auto-edit" || fullAuto != 0 || queued != 0 || worktree.Valid || workspace.Valid || agent.Valid || visibility != "observed" {
		t.Errorf("the F1 thread came out as mode=%s full_auto=%d queued=%d worktree=%v workspace=%v agent=%v visibility=%s",
			mode, fullAuto, queued, worktree, workspace, agent, visibility)
	}
	var callVia, blockVia string
	var decision sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT via, decision FROM tool_calls WHERE id = 'tc_old'`).Scan(&callVia, &decision); err != nil {
		t.Fatalf("read the tool call's new columns: %v", err)
	}
	if callVia != "umbral" || decision.Valid {
		t.Errorf("the F1 tool call came out via=%s decision=%v, want umbral and NULL", callVia, decision)
	}
	if err := db.QueryRowContext(ctx, `SELECT via FROM blocks WHERE id = 'blk_old'`).Scan(&blockVia); err != nil || blockVia != "shell" {
		t.Errorf("the F1 block came out via=%q (%v), want shell", blockVia, err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM approvals WHERE id = 'apr_old' AND state = 'approved'`); n != 1 {
		t.Error("the F1 approval did not survive the upgrade")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Error("foreign_key_check reports a violation after the upgrade")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestMigration0007Constraints_REQ_WT_001: the worktrees table and the new columns refuse what
// the Data Model says they refuse. The new columns carry their CHECKs in the ALTER itself, so a
// writer that forgets the rule fails here rather than storing a state nothing reads.
func TestMigration0007Constraints_REQ_WT_001(t *testing.T) {
	t.Parallel()
	db := openTestStore(t).DB()
	ctx := t.Context()
	for _, stmt := range []string{
		`INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_a', '/repo', 1, 1)`,
		`INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_b', '/repo', 1, 1)`,
		`INSERT INTO workspaces(id, label, cwd, created_at) VALUES ('w1', 'api', '/repo', 1)`,
		`INSERT INTO messages(id, thread_id, turn_id, role, content, created_at) VALUES ('msg_a', 'thr_a', 'trn_1', 'assistant', 'x', 1)`,
		`INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at) VALUES ('ses_a', '/bin/sh', '/repo', 80, 24, 'alive', 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	worktreeOf := func(id, thread, path, state string) string {
		return `INSERT INTO worktrees(id, thread_id, repo_root, repo_id, base_commit, origin_branch, branch, path, state, created_at, updated_at)
			VALUES ('` + id + `', '` + thread + `', '/repo', '0123456789abcdef', 'abc123', 'main', 'umbral/` + thread + `', '` + path + `', '` + state + `', 1, 1)`
	}
	worktree := func(id, state string) string { return worktreeOf(id, "thr_a", "/data/wt", state) }
	for _, c := range []struct{ name, stmt string }{
		{"a valid worktree", worktree("wtr_1", "active")},
		{"a thread in its worktree", `UPDATE threads SET worktree_id = 'wtr_1', workspace_id = 'w1', full_auto = 1, queued = 1, agent = 'opencode', egress_visibility = 'unobserved' WHERE id = 'thr_a'`},
		{"an ACP tool call", `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at, via, decision) VALUES ('tc_a', 'thr_a', 'msg_a', 'edit', 'WriteFS', '{}', 'ok', 1, 'acp', 'unmediated')`},
		{"an ACP terminal block", `INSERT INTO blocks(id, session_id, origin, thread_id, state, started_at, via) VALUES ('blk_a', 'ses_a', 'agent', 'thr_a', 'running', 1, 'acp')`},
		{"a merged worktree", worktree("wtr_2", "merged")},
	} {
		if _, err := db.ExecContext(ctx, c.stmt); err != nil {
			t.Errorf("%s was refused: %v", c.name, err)
		}
	}
	for name, stmt := range map[string]string{
		"an unknown worktree state":   worktree("wtr_3", "lost"),
		"a wait prefix":               worktreeOf("wt_4", "thr_b", "/data/wt-b", "active"),
		"a second active on a thread": worktreeOf("wtr_6", "thr_a", "/data/wt-other", "active"),
		"a second active on a path":   worktreeOf("wtr_7", "thr_b", "/data/wt", "active"),
		"a worktree of no thread":     `INSERT INTO worktrees(id, thread_id, repo_root, repo_id, base_commit, origin_branch, branch, path, state, created_at, updated_at) VALUES ('wtr_5', 'thr_none', '/r', 'x', 'y', 'main', 'b', '/p', 'active', 1, 1)`,
		"a full_auto that is not 0/1": `UPDATE threads SET full_auto = 2`,
		"a queued that is not 0/1":    `UPDATE threads SET queued = 2`,
		"an unknown visibility":       `UPDATE threads SET egress_visibility = 'partial'`,
		"a missing worktree id":       `UPDATE threads SET worktree_id = 'wtr_none'`,
		"a missing workspace":         `UPDATE threads SET workspace_id = 'w9'`,
		"an unknown tool-call via":    `UPDATE tool_calls SET via = 'mcp'`,
		"an unknown decision":         `UPDATE tool_calls SET decision = 'maybe'`,
		"an unknown block via":        `UPDATE blocks SET via = 'acp2'`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestAQueuedTurnIsRecoveredStopped_REQ_ORC_002: a turn held as `queued` when the daemon
// stopped is recovered like a running one — the thread `stopped`, attention `idle` — and loses
// its queued mark, since nothing is holding it any more (REQ-ORC-002, REQ-AGT-017).
func TestAQueuedTurnIsRecoveredStopped_REQ_ORC_002(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	if _, err := s.DB().ExecContext(t.Context(), `INSERT INTO threads(id, cwd, state, attention_state, queued, created_at, updated_at) VALUES
		('thr_queued', '/r', 'idle', 'working', 1, 1, 1),
		('thr_idle', '/r', 'idle', 'done', 0, 1, 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	report, err := s.Recover(t.Context(), time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.ThreadsStopped != 1 {
		t.Errorf("ThreadsStopped = %d, want 1 (the queued turn)", report.ThreadsStopped)
	}
	for id, want := range map[string][3]any{
		"thr_queued": {"stopped", "idle", 0},
		"thr_idle":   {"idle", "done", 0},
	} {
		var state, attention string
		var queued int
		if err := s.DB().QueryRowContext(t.Context(), `SELECT state, attention_state, queued FROM threads WHERE id = ?`, id).
			Scan(&state, &attention, &queued); err != nil {
			t.Fatal(err)
		}
		if got := [3]any{state, attention, queued}; got != want {
			t.Errorf("%s recovered as %v, want %v", id, got, want)
		}
	}
}

// TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001: the retention job never deletes a thread
// that still has a worktree row. The worktree's branch and files are the user's unmerged work,
// and its foreign key would refuse the delete anyway.
func TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	exec(t, s, `INSERT INTO threads(id, cwd, state, ephemeral, created_at, updated_at) VALUES ('thr_wt', '/w', 'idle', 1, ?, ?)`, daysAgo(2), daysAgo(2))
	exec(t, s, `INSERT INTO worktrees(id, thread_id, repo_root, repo_id, base_commit, origin_branch, branch, path, state, created_at, updated_at)
		VALUES ('wtr_1', 'thr_wt', '/repo', '0123456789abcdef', 'abc', 'main', 'umbral/thr_wt', '/data/wt', 'active', 1, 1)`)
	report, err := s.ApplyRetention(t.Context(), retentionNow, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if report.EphemeralThreads != 0 || count(t, s, `SELECT count(*) FROM threads WHERE id = 'thr_wt'`) != 1 {
		t.Error("a thread holding a worktree was purged")
	}
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}
