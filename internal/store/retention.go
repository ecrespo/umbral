package store

import (
	"context"
	"fmt"
	"time"
)

// Retention is Data Model §4's windows, in days. `[retention]` in config.toml sets the
// configurable ones; DefaultRetention is what applies without it.
type Retention struct {
	// RawOutputDays is how long a closed block keeps its raw chunks (`raw_output_days`).
	RawOutputDays int
	// PlainOutputDays is how long a closed block keeps its transcript (`plain_output_days`).
	PlainOutputDays int
	// ClosedStructureDays is how long a closed workspace, tab or pane is kept
	// (`closed_structure_days`).
	ClosedStructureDays int
	// AuditDays is how long `egress_log` and `usage` rows are kept (`audit_days`).
	AuditDays int
}

// DefaultRetention is Data Model §4's defaults.
func DefaultRetention() Retention {
	return Retention{RawOutputDays: 30, PlainOutputDays: 180, ClosedStructureDays: 30, AuditDays: 365}
}

// EphemeralThreadTTL is how long an ephemeral thread (`umb ai`) is kept after its last
// activity. Data Model §4 makes it fixed.
const EphemeralThreadTTL = 24 * time.Hour

// PaneMetadataMaxAge is the latest a pane metadata key lives, whatever TTL its report gave.
const PaneMetadataMaxAge = 24 * time.Hour

// retentionBatch bounds the rows one write transaction of the job touches. Each batch commits
// on its own, so the job never holds the writer for long while a turn is persisting.
const retentionBatch = 500

// RetentionReport counts what one run of the job removed. The daemon logs it.
type RetentionReport struct {
	RawChunks        int64 // block_chunks rows deleted
	Transcripts      int64 // blocks whose output_plain was cleared
	EphemeralThreads int64 // ephemeral threads deleted
	ClosedStructure  int64 // closed workspaces, tabs and panes deleted (cascades not counted)
	PaneMetadata     int64 // pane_metadata keys past their TTL
	AuditRows        int64 // egress_log and usage rows deleted
}

// ApplyRetention runs Data Model §4's maintenance job once, as of now, then `PRAGMA optimize`
// and an FTS optimize.
//
//   - **Raw output.** A closed block (finished or abandoned) keeps its chunks RawOutputDays
//     after it ended, or after it started when no end was recorded. The block's row, size and
//     transcript stay: `block.get` with `include: raw` then answers an empty output whose
//     `output_bytes` still says how much there was.
//   - **Transcripts.** PlainOutputDays after the end, `output_plain` is cleared, which takes it
//     out of the search index through the FTS trigger; the command stays searchable.
//   - **Ephemeral threads.** One idle for EphemeralThreadTTL — not running nor awaiting an
//     approval, owning no live session and no open block — is deleted with what is only its: approvals, tool
//     calls, agent blocks, messages, thread rules. Its usage rows stay as audit with no thread,
//     and the session its commands ran in stays, released from its owner.
//   - **Closed structure.** Workspaces, then tabs, then panes closed for ClosedStructureDays are
//     deleted; what they hold cascades. A purged number can be reused by a later workspace,
//     tab or pane: REQ-WS-002 makes it unique while the object exists.
//   - **Pane metadata** past its own `expires_at`, or written more than 24 h ago.
//   - **Audit.** `egress_log` and `usage` rows older than AuditDays.
//
// Running blocks and threads with a turn are never touched. Each step works in batches of
// retentionBatch rows, one transaction each, so a large purge does not hold the writer.
func (s *Store) ApplyRetention(ctx context.Context, now time.Time, r Retention) (RetentionReport, error) {
	var report RetentionReport
	day := 24 * time.Hour
	cutoff := func(days int) int64 { return now.Add(-time.Duration(days) * day).UnixMilli() }

	steps := []struct {
		what  string
		query string
		args  []any
		count *int64
	}{
		{
			"purge raw output",
			// Bounded in chunk rows, not blocks: one block can hold 256 of them.
			`DELETE FROM block_chunks WHERE (block_id, seq) IN (
				SELECT c.block_id, c.seq FROM block_chunks c JOIN blocks b ON b.id = c.block_id
				 WHERE b.state IN ('finished','abandoned') AND coalesce(b.ended_at, b.started_at) < ?
				 LIMIT ?)`,
			[]any{cutoff(r.RawOutputDays)},
			&report.RawChunks,
		},
		{
			"clear old transcripts",
			`UPDATE blocks SET output_plain = NULL WHERE rowid IN (
				SELECT rowid FROM blocks
				 WHERE output_plain IS NOT NULL AND state IN ('finished','abandoned')
				   AND coalesce(ended_at, started_at) < ?
				 LIMIT ?)`,
			[]any{cutoff(r.PlainOutputDays)},
			&report.Transcripts,
		},
		{
			"purge closed workspaces",
			`DELETE FROM workspaces WHERE id IN (SELECT id FROM workspaces WHERE closed_at < ? LIMIT ?)`,
			[]any{cutoff(r.ClosedStructureDays)},
			&report.ClosedStructure,
		},
		{
			"purge closed tabs",
			`DELETE FROM tabs WHERE id IN (SELECT id FROM tabs WHERE closed_at < ? LIMIT ?)`,
			[]any{cutoff(r.ClosedStructureDays)},
			&report.ClosedStructure,
		},
		{
			"purge closed panes",
			`DELETE FROM panes WHERE id IN (SELECT id FROM panes WHERE closed_at < ? LIMIT ?)`,
			[]any{cutoff(r.ClosedStructureDays)},
			&report.ClosedStructure,
		},
		{
			"purge expired pane metadata",
			`DELETE FROM pane_metadata WHERE rowid IN (
				SELECT rowid FROM pane_metadata WHERE expires_at < ? OR updated_at < ? LIMIT ?)`,
			[]any{now.UnixMilli(), now.Add(-PaneMetadataMaxAge).UnixMilli()},
			&report.PaneMetadata,
		},
		{
			"purge old egress records",
			`DELETE FROM egress_log WHERE id IN (SELECT id FROM egress_log WHERE created_at < ? LIMIT ?)`,
			[]any{cutoff(r.AuditDays)},
			&report.AuditRows,
		},
		{
			"purge old usage records",
			`DELETE FROM usage WHERE id IN (SELECT id FROM usage WHERE created_at < ? LIMIT ?)`,
			[]any{cutoff(r.AuditDays)},
			&report.AuditRows,
		},
	}
	for _, step := range steps {
		n, err := s.batched(ctx, step.query, step.args...)
		*step.count += n
		if err != nil {
			return report, fmt.Errorf("store: %s: %w", step.what, err)
		}
	}

	n, err := s.purgeEphemeralThreads(ctx, now.Add(-EphemeralThreadTTL).UnixMilli())
	report.EphemeralThreads = n
	if err != nil {
		return report, fmt.Errorf("store: purge ephemeral threads: %w", err)
	}

	for _, q := range []string{`PRAGMA optimize`, `INSERT INTO blocks_fts(blocks_fts) VALUES('optimize')`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return report, fmt.Errorf("store: %s: %w", q, err)
		}
	}
	return report, nil
}

// batched runs a statement whose last parameter is a LIMIT until it affects no row, one
// transaction per run, and returns how many rows it affected in all.
func (s *Store) batched(ctx context.Context, query string, args ...any) (int64, error) {
	var total int64
	args = append(args, retentionBatch)
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		result, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return total, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if s.onRetentionBatch != nil {
			s.onRetentionBatch(n)
		}
		if n == 0 {
			return total, nil
		}
	}
}

// purgeEphemeralThreads deletes the ephemeral threads idle since before cutoff, one
// transaction each, and returns how many it deleted. A thread that stopped qualifying between
// the listing and its own transaction — a send landed, a session came alive — is skipped, and
// so is one whose transaction fails: one busy thread does not stop the job.
func (s *Store) purgeEphemeralThreads(ctx context.Context, cutoff int64) (int64, error) {
	var total int64
	skipped := map[string]bool{}
	for {
		ids, err := s.idleEphemeralThreads(ctx, cutoff, skipped)
		if err != nil || len(ids) == 0 {
			return total, err
		}
		for _, id := range ids {
			deleted, err := s.deleteThread(ctx, id, cutoff)
			if err := ctx.Err(); err != nil {
				return total, err
			}
			if err != nil || !deleted {
				skipped[id] = true
				continue
			}
			total++
		}
	}
}

// idleThread is the condition a purged ephemeral thread meets, checked when listing and again
// inside the transaction that deletes it.
const idleThread = `t.ephemeral = 1 AND t.updated_at < ?
		   AND t.state NOT IN ('running','awaiting_approval')
		   AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.owner_thread_id = t.id AND s.state = 'alive')
		   AND NOT EXISTS (SELECT 1 FROM blocks b WHERE b.thread_id = t.id AND b.state IN ('running','interactive'))`

func (s *Store) idleEphemeralThreads(ctx context.Context, cutoff int64, skipped map[string]bool) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM threads t WHERE `+idleThread+` ORDER BY t.id`, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() && len(ids) < retentionBatch {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !skipped[id] {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// deleteThread deletes one thread and what only it holds, if it still meets idleThread inside
// the transaction, in the order the foreign keys allow: approvals reference tool calls, tool
// calls reference blocks, and blocks and sessions reference the thread without a cascade.
// Messages, thread rules and the rest cascade from the thread; usage rows keep their audit
// with a NULL thread. It reports whether the thread was deleted.
func (s *Store) deleteThread(ctx context.Context, id string, cutoff int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var idle int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM threads t WHERE t.id = ? AND `+idleThread, id, cutoff).Scan(&idle); err != nil {
		return false, err
	}
	if idle == 0 {
		return false, nil
	}
	for _, q := range []string{
		`DELETE FROM approvals WHERE thread_id = ?`,
		`DELETE FROM tool_calls WHERE thread_id = ?`,
		`DELETE FROM blocks WHERE thread_id = ? AND state IN ('finished','abandoned')`,
		`UPDATE sessions SET owner_thread_id = NULL WHERE owner_thread_id = ? AND state = 'exited'`,
		`DELETE FROM threads WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return false, fmt.Errorf("%s: %w", q, err)
		}
	}
	return true, tx.Commit()
}
