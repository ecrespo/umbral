package reportstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/ecrespo/umbral/internal/integrations/domain"
)

// WriteMetadata implements ports.MetadataStore. A source's metadata seq lives in a reserved
// row of its own (domain.KeySeq) rather than in a column `pane_metadata` does not have: it
// moves with the pane and is cleared at start with the rest, without a migration.
func (s *Store) WriteMetadata(ctx context.Context, r domain.MetadataReport, now int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("reportstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stored string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM pane_metadata WHERE pane_id = ? AND source = ? AND key = ?`,
		r.PaneID, r.Source, domain.KeySeq).Scan(&stored)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("reportstore: read the last metadata seq: %w", err)
	}
	last := int64(unnumbered)
	if exists {
		if last, err = strconv.ParseInt(stored, 10, 64); err != nil {
			return false, fmt.Errorf("reportstore: stored metadata seq %q is not a number: %w", stored, err)
		}
	}
	if domain.Stale(r.Seq, last, exists) {
		return false, nil
	}
	if r.Seq != nil {
		last = *r.Seq
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pane_metadata WHERE pane_id = ? AND expires_at IS NOT NULL AND expires_at <= ?`,
		r.PaneID, now); err != nil {
		return false, fmt.Errorf("reportstore: drop expired metadata: %w", err)
	}
	// Strictly increasing per pane, as for the reports: when two sources set one key in the
	// same millisecond, the later write is still the one shown.
	var newest sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT max(updated_at) FROM pane_metadata WHERE pane_id = ?`, r.PaneID).Scan(&newest); err != nil {
		return false, fmt.Errorf("reportstore: read the newest metadata: %w", err)
	}
	if newest.Valid && now <= newest.Int64 {
		now = newest.Int64 + 1
	}
	// Every displayed key expires: at its TTL, or a day after its last write without one.
	lifetime := r.TTLMillis
	if lifetime <= 0 {
		lifetime = domain.MaxTTLMillis
	}
	expires := sql.NullInt64{Int64: now + lifetime, Valid: true}

	for _, key := range r.Clear {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pane_metadata WHERE pane_id = ? AND source = ? AND key = ?`,
			r.PaneID, r.Source, key); err != nil {
			return false, fmt.Errorf("reportstore: clear %s: %w", key, err)
		}
	}
	for _, prefix := range r.ClearPrefixes {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pane_metadata WHERE pane_id = ? AND source = ? AND substr(key, 1, ?) = ?`,
			r.PaneID, r.Source, len(prefix), prefix); err != nil {
			return false, fmt.Errorf("reportstore: clear %s*: %w", prefix, err)
		}
	}
	for key, value := range r.Set {
		if err := upsertMetadata(ctx, tx, r.PaneID, r.Source, key, value, expires, now); err != nil {
			return false, err
		}
	}
	if err := upsertMetadata(ctx, tx, r.PaneID, r.Source, domain.KeySeq,
		strconv.FormatInt(last, 10), sql.NullInt64{}, now); err != nil {
		return false, err
	}

	var live int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM pane_metadata
		 WHERE pane_id = ? AND key <> ? AND (expires_at IS NULL OR expires_at > ?)`,
		r.PaneID, domain.KeySeq, now).Scan(&live); err != nil {
		return false, fmt.Errorf("reportstore: count the live metadata: %w", err)
	}
	if live > domain.MaxLiveKeysPerPane {
		return false, fmt.Errorf("%w: pane %s would hold %d live metadata keys, more than %d",
			domain.ErrValidation, r.PaneID, live, domain.MaxLiveKeysPerPane)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("reportstore: commit: %w", err)
	}
	return true, nil
}

func upsertMetadata(ctx context.Context, tx *sql.Tx, paneID, source, key, value string, expires sql.NullInt64, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pane_metadata(pane_id, source, key, value, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(pane_id, source, key) DO UPDATE SET
		  value = excluded.value, expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		paneID, source, key, value, expires, now); err != nil {
		return fmt.Errorf("reportstore: write %s: %w", key, err)
	}
	return nil
}
