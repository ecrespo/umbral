package treestore

import (
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

// maxScreenBytes bounds what a stored screen may decompress to.
//
// A screen is the visible grid plus a little scrollback, so a few hundred kilobytes is
// generous; 16 MiB is the point at which a row is wrong rather than large, and refusing it is
// better than allocating whatever a corrupt blob claims to need.
const maxScreenBytes = 16 << 20

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

	// A decoder with no reader: `DecodeAll` works on the slice and never touches the
	// stream, so handing it one would spawn decode goroutines for a source nothing reads.
	// The memory bound is not decoration — the blob is read back from disk and a corrupt
	// or hostile row should fail rather than ask for an arbitrary allocation.
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxScreenBytes))
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

// The three statements that honour `pane_history`'s retention, one per way a pane stops
// being open. They are written out in full rather than composed from a fragment: a DELETE
// assembled by concatenation is the shape `gosec` G202 exists to flag, and being able to read
// each statement whole is worth more here than sharing six words between them.
const (
	forgetScreenOfPane = `DELETE FROM pane_history WHERE pane_id = ?`
	forgetScreensOfTab = `DELETE FROM pane_history
		 WHERE pane_id IN (SELECT id FROM panes WHERE tab_id = ?)`
	forgetScreensOfWorkspace = `DELETE FROM pane_history
		 WHERE pane_id IN (SELECT p.id FROM panes p
		                     JOIN tabs t ON t.id = p.tab_id
		                    WHERE t.workspace_id = ?)`
)

// forgetScreens runs one of the statements above inside the caller's transaction.
//
// Data Model §4 gives `pane_history` a retention of "until the pane closes", and closing is
// the one thing that cannot reach the rows on its own: a pane is closed with
// `UPDATE panes SET closed_at = ?`, never deleted, so the table's ON DELETE CASCADE never
// fires. Without this the screens of every pane the user ever closed would accumulate for as
// long as the installation lives — exactly the data REQ-TERM-010 is disabled by default to be
// careful with.
//
// It runs in the closing transaction rather than in a sweeper so that the row is gone by the
// time the call returns: a screen that survives its pane for a while is still a screen that
// outlived the promise.
func forgetScreens(ctx context.Context, tx *sql.Tx, query, id string) error {
	if _, err := tx.ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("treestore: forget the screens of the closed panes: %w", err)
	}
	return nil
}
