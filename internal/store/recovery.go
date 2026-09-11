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
}

// Recover applies steps 1 and 2 of Data Model §6 after a daemon restart.
//
// A session row only ever means "there is a live PTY behind it", and a restart kills
// every PTY the daemon owned. Leaving a row as alive would make the next `session.list`
// advertise sessions that cannot receive input. The blocks those sessions had open are
// marked abandoned rather than deleted: their output is the user's history (REQ-TERM-005).
//
// Steps 3 and 4 of §6, for threads and approvals, arrive with the agent subdomain in
// T-F1-01. The tables exist but F0 never writes to them.
//
// The two updates share one transaction so a crash mid-recovery cannot leave sessions
// exited while their blocks still claim to be running.
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
