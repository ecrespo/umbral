package store

import (
	"context"
	"fmt"
	"time"
)

// RecoveryReport counts what a restart had to clean up. The daemon logs it, and
// `umb status` can surface it, so an operator can tell a clean restart from a crash.
type RecoveryReport struct {
	// SessionsExited is how many sessions were still marked alive. Their PTYs died with
	// the previous process, so the rows were lying.
	SessionsExited int64
	// BlocksAbandoned is how many blocks were still running or interactive. Their output
	// is kept, which is what REQ-TERM-005 promises; only the state changes.
	BlocksAbandoned int64
	// IntegrationSettled is how many sessions still had an integration verdict pending.
	// None is alive now, so nothing will ever judge them: those with blocks become `osc133`,
	// the rest `none` (REQ-BLK-003).
	// On the first start after upgrading past T-F0-21 it also counts the rows an older
	// daemon left pending.
	IntegrationSettled int64
}

// Recover applies steps 1 and 2 of Data Model §6 after a daemon restart, step 1 including
// the integration verdict a crash interrupted (delta `2026-09-recovery-integration`).
//
// A session row only ever means "there is a live PTY behind it", and a restart kills
// every PTY the daemon owned. Leaving a row as alive would make the next `session.list`
// advertise sessions that cannot receive input. The blocks those sessions had open are
// marked abandoned rather than deleted: their output is the user's history (REQ-TERM-005).
//
// Steps 3 and 4 of §6, for threads and approvals, arrive with the agent subdomain in
// T-F1-01. The tables exist but F0 never writes to them.
//
// The updates share one transaction so a crash mid-recovery cannot leave sessions exited
// while their blocks still claim to be running, or their verdict still pending.
func (s *Store) Recover(ctx context.Context, now time.Time) (RecoveryReport, error) {
	var report RecoveryReport

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("store: begin recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	sessions, err := tx.ExecContext(ctx,
		`UPDATE sessions SET state = 'exited', exit_code = NULL, exited_at = ? WHERE state = 'alive'`,
		nowMillis(now))
	if err != nil {
		return report, fmt.Errorf("store: mark alive sessions exited: %w", err)
	}
	if report.SessionsExited, err = sessions.RowsAffected(); err != nil {
		return report, fmt.Errorf("store: count recovered sessions: %w", err)
	}

	// Still step 1: with no session alive, a verdict left pending belongs to a process that
	// can no longer be judged. A session with blocks spoke OSC 133 — its block row is written
	// before the verdict that follows it, and a crash or a failed write can come between —
	// so it is `osc133`; every other one emitted no marker, and REQ-BLK-003's verdict for that
	// is `none`. Over the whole table, so a row an older daemon left pending is repaired too;
	// `osc133` rows are never touched, which is the one-way rule.
	for _, verdict := range []struct{ name, query string }{
		{"osc133", `UPDATE sessions SET integration = 'osc133' WHERE integration = 'pending'
			AND EXISTS (SELECT 1 FROM blocks WHERE blocks.session_id = sessions.id)`},
		{"none", `UPDATE sessions SET integration = 'none' WHERE integration = 'pending'`},
	} {
		settled, err := tx.ExecContext(ctx, verdict.query)
		if err != nil {
			return report, fmt.Errorf("store: settle pending integration verdicts as %s: %w", verdict.name, err)
		}
		n, err := settled.RowsAffected()
		if err != nil {
			return report, fmt.Errorf("store: count integration verdicts settled as %s: %w", verdict.name, err)
		}
		report.IntegrationSettled += n
	}

	blocks, err := tx.ExecContext(ctx,
		`UPDATE blocks SET state = 'abandoned' WHERE state IN ('running','interactive')`)
	if err != nil {
		return report, fmt.Errorf("store: mark open blocks abandoned: %w", err)
	}
	if report.BlocksAbandoned, err = blocks.RowsAffected(); err != nil {
		return report, fmt.Errorf("store: count recovered blocks: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("store: commit recovery: %w", err)
	}
	return report, nil
}
