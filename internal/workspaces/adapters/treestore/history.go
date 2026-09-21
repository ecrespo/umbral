package treestore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// Pane history: the opt-in screen replay of REQ-TERM-010 (Data Model §2.4d).
//
// One row per pane, replaced on each capture rather than appended. The requirement asks for
// "the stored recent screen", not a history, and an append-only table would grow without
// bound while holding exactly the secrets the setting is disabled by default to avoid.

// SaveScreen records a pane's screen, compressed.
func (s *Store) SaveScreen(ctx context.Context, paneID string, screen []byte, rows int, atMillis int64) error {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return fmt.Errorf("treestore: compress the screen of %s: %w", paneID, err)
	}
	compressed := encoder.EncodeAll(screen, nil)
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("treestore: finish compressing the screen of %s: %w", paneID, err)
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO pane_history(pane_id, screen_zst, rows, captured_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(pane_id) DO UPDATE SET
			screen_zst = excluded.screen_zst,
			rows = excluded.rows,
			captured_at = excluded.captured_at`,
		paneID, compressed, rows, atMillis); err != nil {
		return fmt.Errorf("treestore: store the screen of %s: %w", paneID, err)
	}
	return nil
}

// LoadScreen returns a pane's stored screen, or nil when there is none.
func (s *Store) LoadScreen(ctx context.Context, paneID string) ([]byte, error) {
	var compressed []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT screen_zst FROM pane_history WHERE pane_id = ?`, paneID).Scan(&compressed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("treestore: read the screen of %s: %w", paneID, err)
	}

	decoder, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("treestore: read the screen of %s: %w", paneID, err)
	}
	defer decoder.Close()

	screen, err := decoder.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("treestore: decompress the screen of %s: %w", paneID, err)
	}
	return screen, nil
}

// ForgetScreens empties the table.
//
// Data Model §2.4d: "Turning the setting off deletes the table's contents at the next start."
// That is a privacy promise and not housekeeping — a user who turns pane history off is
// asking for what was captured to stop existing, and leaving the rows would answer a
// different question.
func (s *Store) ForgetScreens(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pane_history`); err != nil {
		return fmt.Errorf("treestore: clear the stored screens: %w", err)
	}
	return nil
}
