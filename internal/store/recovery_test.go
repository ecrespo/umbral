package store

import (
	"testing"
	"time"
)

// TestRecoveryMarksOpenBlocksAbandoned_REQ_TERM_005 covers Data Model §6 steps 1 and 2.
//
// REQ-TERM-005 promises that a session's blocks survive the shell exiting. A restart is
// the harshest version of that: every PTY is gone, so every alive session is a lie, but
// the blocks and their output must still be there, marked abandoned rather than deleted.
func TestRecoveryMarksOpenBlocksAbandoned_REQ_TERM_005(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)

	alive := insertSession(t, s, "ses_alive", "alive")
	exited := insertSession(t, s, "ses_exited", "exited")
	running := insertBlock(t, s, "blk_running", alive, "running", "sleep 600", "")
	interactive := insertBlock(t, s, "blk_interactive", alive, "interactive", "vim main.go", "")
	finished := insertBlock(t, s, "blk_finished", exited, "finished", "go build ./...", "ok")

	restartedAt := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	report, err := s.Recover(t.Context(), restartedAt)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if report.SessionsExited != 1 {
		t.Errorf("SessionsExited = %d, want 1", report.SessionsExited)
	}
	if report.BlocksAbandoned != 2 {
		t.Errorf("BlocksAbandoned = %d, want 2", report.BlocksAbandoned)
	}

	for _, tc := range []struct{ id, want string }{
		{running, "abandoned"},
		{interactive, "abandoned"},
		{finished, "finished"},
	} {
		var state string
		if err := s.DB().QueryRowContext(t.Context(),
			"SELECT state FROM blocks WHERE id = ?", tc.id).Scan(&state); err != nil {
			t.Fatalf("read block %s: %v", tc.id, err)
		}
		if state != tc.want {
			t.Errorf("block %s state = %q, want %q", tc.id, state, tc.want)
		}
	}

	// The output must survive: REQ-TERM-005 keeps the blocks, it does not discard them.
	var blocks int
	if err := s.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM blocks").Scan(&blocks); err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if blocks != 3 {
		t.Errorf("blocks after recovery = %d, want 3; recovery must not delete history", blocks)
	}

	var (
		state    string
		exitCode *int64
		exitedAt *int64
	)
	if err := s.DB().QueryRowContext(t.Context(),
		"SELECT state, exit_code, exited_at FROM sessions WHERE id = ?", alive).
		Scan(&state, &exitCode, &exitedAt); err != nil {
		t.Fatalf("read session %s: %v", alive, err)
	}
	if state != "exited" {
		t.Errorf("session %s state = %q, want exited", alive, state)
	}
	if exitCode != nil {
		t.Errorf("session %s exit_code = %v, want NULL: nobody observed how it died", alive, *exitCode)
	}
	if exitedAt == nil {
		t.Fatalf("session %s exited_at is NULL, want the restart time", alive)
	}
	if got := *exitedAt; got != restartedAt.UnixMilli() {
		t.Errorf("session %s exited_at = %d, want %d (UTC epoch ms, Art. 6)", alive, got, restartedAt.UnixMilli())
	}
}

// TestRecoveryOnACleanDatabaseIsANoOp checks the ordinary case: a daemon that shut down
// cleanly finds nothing to repair, and says so.
func TestRecoveryOnACleanDatabaseIsANoOp(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	session := insertSession(t, s, "ses_exited", "exited")
	insertBlock(t, s, "blk_finished", session, "finished", "ls", "a\nb")

	report, err := s.Recover(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.SessionsExited != 0 || report.BlocksAbandoned != 0 {
		t.Errorf("Recover on a clean database = %+v, want zeroes", report)
	}
}

// TestRecoveryIsIdempotent runs recovery twice: the second pass must find nothing, or a
// crash loop would keep rewriting exited_at and hide when the session really died.
func TestRecoveryIsIdempotent(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	alive := insertSession(t, s, "ses_alive", "alive")
	insertBlock(t, s, "blk_running", alive, "running", "sleep 600", "")

	first := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	if _, err := s.Recover(t.Context(), first); err != nil {
		t.Fatalf("first Recover: %v", err)
	}
	report, err := s.Recover(t.Context(), first.Add(time.Hour))
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	if report.SessionsExited != 0 || report.BlocksAbandoned != 0 {
		t.Errorf("second Recover = %+v, want zeroes", report)
	}

	var exitedAt int64
	if err := s.DB().QueryRowContext(t.Context(),
		"SELECT exited_at FROM sessions WHERE id = ?", alive).Scan(&exitedAt); err != nil {
		t.Fatalf("read exited_at: %v", err)
	}
	if exitedAt != first.UnixMilli() {
		t.Errorf("exited_at = %d after a second recovery, want the first timestamp %d", exitedAt, first.UnixMilli())
	}
}

// TestRecoverySettlesAPendingIntegration_REQ_BLK_003 is Data Model §6 step 1 as delta
// `2026-09-recovery-integration` completes it.
//
// REQ-BLK-003 gives a session that emits no OSC 133 the verdict `none`. T-F0-21 settles it when
// a session exits, but a daemon that dies runs no exit path: a session still inside its
// five-second window at a `kill -9` came back `exited|pending`, and nothing would ever judge
// it again. Rows an older daemon wrote carry the same lie, which is why the exited one here
// is repaired too, not only the one this recovery exits.
func TestRecoverySettlesAPendingIntegration_REQ_BLK_003(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	for _, row := range []struct{ id, state, integration string }{
		{"ses_crashed_in_window", "alive", "pending"},
		{"ses_left_by_old_daemon", "exited", "pending"},
		{"ses_integrated", "alive", "osc133"},
		{"ses_judged", "exited", "none"},
		{"ses_blocks_before_verdict", "alive", "pending"},
	} {
		insertSession(t, s, row.id, row.state)
		if _, err := s.DB().ExecContext(t.Context(),
			"UPDATE sessions SET integration = ? WHERE id = ?", row.integration, row.id); err != nil {
			t.Fatalf("set %s integration: %v", row.id, err)
		}
	}

	// A block row is written before the osc133 verdict that follows it, and that write can
	// fail or be cut off by the crash. A session with blocks spoke OSC 133 whatever its row
	// says, and calling it `none` would be the disagreement REQ-BLK-003 exists to prevent.
	insertBlock(t, s, "blk_before_verdict", "ses_blocks_before_verdict", "finished", "ls", "a")

	report, err := s.Recover(t.Context(), time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.IntegrationSettled != 3 {
		t.Errorf("IntegrationSettled = %d, want 3", report.IntegrationSettled)
	}

	for id, want := range map[string]string{
		"ses_crashed_in_window":     "none",
		"ses_left_by_old_daemon":    "none",
		"ses_integrated":            "osc133", // the one-way rule: never back to none
		"ses_judged":                "none",
		"ses_blocks_before_verdict": "osc133",
	} {
		var state, integration string
		if err := s.DB().QueryRowContext(t.Context(),
			"SELECT state, integration FROM sessions WHERE id = ?", id).Scan(&state, &integration); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if integration != want {
			t.Errorf("%s integration = %q, want %q", id, integration, want)
		}
		if state != "exited" {
			t.Errorf("%s state = %q, want exited", id, state)
		}
	}

	again, err := s.Recover(t.Context(), time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	if again.IntegrationSettled != 0 {
		t.Errorf("a second recovery settled %d verdicts, want 0", again.IntegrationSettled)
	}
}

// TestRecoveryExpiresPendingApprovals_REQ_AGT_011 is Data Model §6 steps 3 and 4.
//
// REQ-AGT-011 persists every approval before a client sees it, which is what makes this
// necessary: after a restart the row still says `pending`, and the turn that was waiting for
// it died with the previous process. Left alone, `approval.list` would offer a decision
// nothing is waiting for, and approving it would run a tool call no turn will read. So the
// approval expires and its thread stops — keeping its history, so `thread.send` resumes it
// (REQ-AGT-017). A decided approval and an idle or stopped thread are left exactly as they
// were.
func TestRecoveryExpiresPendingApprovals_REQ_AGT_011(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	db := s.DB()
	for _, stmt := range []string{
		`INSERT INTO threads(id, cwd, state, created_at, updated_at) VALUES
			('thr_running', '/r', 'running', 1, 1),
			('thr_waiting', '/r', 'awaiting_approval', 1, 1),
			('thr_idle', '/r', 'idle', 1, 1),
			('thr_stopped', '/r', 'stopped', 1, 1)`,
		`INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
			VALUES ('msg_1', 'thr_waiting', 't', 'assistant', 'x', 1)`,
		`INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at) VALUES
			('tc_1', 'thr_waiting', 'msg_1', 'run_command', 'Exec', '{}', 'pending', 1),
			('tc_2', 'thr_waiting', 'msg_1', 'run_command', 'Exec', '{}', 'ok', 1),
			('tc_3', 'thr_waiting', 'msg_1', 'edit_file', 'WriteFS', '{}', 'denied_by_user', 1)`,
		`INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, state, created_at, decided_at) VALUES
			('apr_pending', 'thr_waiting', 'tc_1', 'run_command', 'Exec', 'destructive', 'rm -rf build', 'pending', 1, NULL),
			('apr_approved', 'thr_waiting', 'tc_2', 'run_command', 'Exec', 'policy', 'go test', 'approved', 1, 2),
			('apr_denied', 'thr_waiting', 'tc_3', 'edit_file', 'WriteFS', 'policy', 'main.go', 'denied', 1, 2)`,
	} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	restartedAt := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	report, err := s.Recover(t.Context(), restartedAt)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.ThreadsStopped != 2 || report.ApprovalsExpired != 1 {
		t.Errorf("report = %+v, want 2 threads stopped and 1 approval expired", report)
	}

	for id, want := range map[string]string{
		"thr_running": "stopped", "thr_waiting": "stopped", "thr_idle": "idle", "thr_stopped": "stopped",
	} {
		var state string
		if err := db.QueryRowContext(t.Context(), "SELECT state FROM threads WHERE id = ?", id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("%s is %s after recovery, want %s", id, state, want)
		}
	}
	for id, want := range map[string]string{
		"apr_pending": "expired", "apr_approved": "approved", "apr_denied": "denied",
	} {
		var state string
		if err := db.QueryRowContext(t.Context(), "SELECT state FROM approvals WHERE id = ?", id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("%s is %s after recovery, want %s", id, state, want)
		}
	}

	// Only what §6 says changes: an expiry is not a decision, so decided_at stays NULL, and
	// a stop does not touch updated_at, so a crash does not reorder `thread.list`.
	var decidedAt *int64
	if err := db.QueryRowContext(t.Context(),
		"SELECT decided_at FROM approvals WHERE id = 'apr_pending'").Scan(&decidedAt); err != nil || decidedAt != nil {
		t.Errorf("the expired approval has decided_at = %v (%v), want NULL", decidedAt, err)
	}
	var updatedAt int64
	if err := db.QueryRowContext(t.Context(),
		"SELECT updated_at FROM threads WHERE id = 'thr_waiting'").Scan(&updatedAt); err != nil || updatedAt != 1 {
		t.Errorf("the stopped thread's updated_at = %d (%v), want it untouched at 1", updatedAt, err)
	}

	// History is kept: the stopped thread's message is still there for thread.send to resume.
	var messages int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM messages WHERE thread_id = 'thr_waiting'").Scan(&messages); err != nil || messages != 1 {
		t.Errorf("the stopped thread has %d messages (%v), want its history kept", messages, err)
	}

	again, err := s.Recover(t.Context(), restartedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	if again.ThreadsStopped != 0 || again.ApprovalsExpired != 0 {
		t.Errorf("second Recover = %+v, want nothing left to stop or expire", again)
	}
}

// TestRecoveryClearsPaneStateAndMetadata_REQ_INT_002 is Data Model §6 step 8: external
// authority and display metadata do not survive a restart (REQ-INT-002, REQ-INT-004). The
// process that reported a pane `working` may be long gone; a restored pane claiming it would
// hold a wait open, or a rollup red, on the word of nobody.
func TestRecoveryClearsPaneStateAndMetadata_REQ_INT_002(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	for _, stmt := range []string{
		`INSERT INTO workspaces(id, label, cwd, created_at) VALUES ('w1', 'api', '/r', 1)`,
		`INSERT INTO tabs(id, workspace_id, label, created_at) VALUES ('w1:t1', 'w1', 'main', 1)`,
		`INSERT INTO panes(id, tab_id, cwd, created_at) VALUES ('w1:p1', 'w1:t1', '/r', 1)`,
		`INSERT INTO pane_state_reports(pane_id, source, state, updated_at) VALUES ('w1:p1', 'claude-code', 'working', 1)`,
		`INSERT INTO pane_metadata(pane_id, source, key, value, updated_at) VALUES ('w1:p1', 'x', 'branch', 'main', 1)`,
	} {
		if _, err := s.DB().ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	report, err := s.Recover(t.Context(), time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.PaneReportsCleared != 2 {
		t.Errorf("PaneReportsCleared = %d, want 2 (one state report, one metadata key)", report.PaneReportsCleared)
	}
	for _, table := range []string{"pane_state_reports", "pane_metadata"} {
		var n int
		if err := s.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s holds %d rows after a restart (%v), want none", table, n, err)
		}
	}
	var panes int
	if err := s.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM panes").Scan(&panes); err != nil || panes != 1 {
		t.Errorf("the pane itself is gone (%d, %v); only what was reported about it may be", panes, err)
	}
}
