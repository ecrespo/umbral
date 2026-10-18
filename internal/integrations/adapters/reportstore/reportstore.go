// Package reportstore keeps external state reports in SQLite (`pane_state_reports`, Data
// Model §4).
package reportstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// Store implements ports.Store.
type Store struct {
	db *sql.DB
}

// New returns a report store over the daemon's database.
func New(db *store.Store) (*Store, error) {
	if db == nil {
		return nil, errors.New("reportstore: a store is required")
	}
	return &Store{db: db.DB()}, nil
}

// Upsert implements ports.Store. The staleness check and the write share a transaction, so
// two reports of one source racing each other cannot both pass a check against the same
// stored seq.
func (s *Store) Upsert(ctx context.Context, r domain.Report, now int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("reportstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var last int64
	err = tx.QueryRowContext(ctx,
		`SELECT seq FROM pane_state_reports WHERE pane_id = ? AND source = ?`,
		r.PaneID, r.Source).Scan(&last)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("reportstore: read the last seq: %w", err)
	}
	if domain.Stale(r.Seq, last, exists) {
		return false, nil
	}

	var newest sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT max(updated_at) FROM pane_state_reports WHERE pane_id = ?`,
		r.PaneID).Scan(&newest); err != nil {
		return false, fmt.Errorf("reportstore: read the newest report: %w", err)
	}
	if newest.Valid && now <= newest.Int64 {
		now = newest.Int64 + 1
	}

	// A source's first report without a seq stores unnumbered, -1: it has accepted no number,
	// so its first numbered report applies even at 0 (REQ-INT-003). Validate rejects a
	// negative seq, so -1 is never a number a source sent.
	seq := last
	if !exists {
		seq = unnumbered
	}
	if r.Seq != nil {
		seq = *r.Seq
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pane_state_reports(pane_id, source, agent, state, message, seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pane_id, source) DO UPDATE SET
		  agent = excluded.agent, state = excluded.state, message = excluded.message,
		  seq = excluded.seq, updated_at = excluded.updated_at`,
		r.PaneID, r.Source, nullable(r.Agent), string(r.State), nullable(r.Message), seq, now); err != nil {
		return false, fmt.Errorf("reportstore: write the report: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("reportstore: commit: %w", err)
	}
	return true, nil
}

// Delete implements ports.Store.
func (s *Store) Delete(ctx context.Context, paneID, source string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM pane_state_reports WHERE pane_id = ? AND source = ?`, paneID, source)
	if err != nil {
		return false, fmt.Errorf("reportstore: delete the report: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("reportstore: count the deleted report: %w", err)
	}
	return n > 0, nil
}

// unnumbered is the stored seq of a source that has sent no number yet.
const unnumbered = -1

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
