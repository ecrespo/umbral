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
