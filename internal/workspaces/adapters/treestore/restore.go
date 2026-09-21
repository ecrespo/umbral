package treestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
	"github.com/ecrespo/umbral/internal/workspaces/ports"
)

// Restore reads what a daemon needs to rebuild the tree it had (REQ-TERM-009, Data Model §6
// step 5).
//
// It clears `panes.session_id` in the same transaction as the read. The sessions those
// columns name belong to the previous run and are already marked `exited` by the store's own
// recovery, so leaving them would give every restored pane a foreign key to a dead terminal —
// and `idx_panes_session` is unique, so the first pane to be given a fresh session would
// collide with nothing while the stale rows quietly failed the next move.
//
// A pane with a stored command comes back with `command_pending` set. That is the whole of
// REQ-TERM-011 on this side: the daemon records that the command is waiting and does not run
// it. Writing it here rather than in the service means a restart interrupted halfway still
// leaves the flag set, which is the safe direction — a pending command shown twice is a
// nuisance, a pending command silently run is the thing the requirement forbids.
func (s *Store) Restore(ctx context.Context) (ports.Restored, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.Restored{}, fmt.Errorf("treestore: begin restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Mark every open pane with a command as pending before anything else reads them, so
	// the panes returned below already carry the flag.
	if _, err := tx.ExecContext(ctx, `
		UPDATE panes SET command_pending = 1
		 WHERE closed_at IS NULL AND command_json IS NOT NULL`); err != nil {
		return ports.Restored{}, fmt.Errorf("treestore: mark pending commands: %w", err)
	}

	panes, err := readRestorablePanes(ctx, tx)
	if err != nil {
		return ports.Restored{}, err
	}
	restored := ports.Restored{Panes: panes}

	if _, err := tx.ExecContext(ctx, `
		UPDATE panes SET session_id = NULL WHERE closed_at IS NULL`); err != nil {
		return ports.Restored{}, fmt.Errorf("treestore: clear the previous run's sessions: %w", err)
	}

	// Focus, as of the last run. The focused workspace is the one focused most recently;
	// its focused tab is the one recorded on it.
	//
	// The tab is checked rather than trusted. `focused_tab_id` is declared ON DELETE SET
	// NULL, which reads like it covers this and does not: closing a tab is an UPDATE, the
	// row is never deleted, and the id outlives its tab. A restart that adopted it would
	// come up focused on a tab that is gone, and `layout.export` with no `tab_id` — whose
	// whole purpose is to resolve the focused tab — would answer "tab w1:t1 does not
	// exist".
	//
	// The ordering is fully determined, which it was not before. `focused_at` is epoch
	// milliseconds (Art. 6), so two workspaces focused inside the same millisecond tied,
	// and SQLite was free to return either — a restart landing on a different workspace
	// depending on the query plan. Within one millisecond the true order is unknowable, so
	// the tie falls to the workspace created last, which is both deterministic and the
	// better guess.
	//
	// When it is gone the workspace keeps focus and falls back to its oldest open tab.
	// Dropping the tab as well would leave the daemon with a focused workspace and no
	// focused tab, which is a state the default handles by failing; landing on a tab the
	// user did not choose is a smaller cost than that, and than losing the workspace too.
	var workspaceID, tabID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT w.id,
		       COALESCE(
		         (SELECT t.id FROM tabs t
		           WHERE t.id = w.focused_tab_id AND t.closed_at IS NULL),
		         (SELECT t2.id FROM tabs t2
		           WHERE t2.workspace_id = w.id AND t2.closed_at IS NULL
		           ORDER BY t2.order_index, t2.created_at LIMIT 1))
		  FROM workspaces w
		 WHERE w.closed_at IS NULL AND w.focused_at IS NOT NULL
		 ORDER BY w.focused_at DESC, w.created_at DESC, w.rowid DESC
		 LIMIT 1`).Scan(&workspaceID, &tabID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ports.Restored{}, fmt.Errorf("treestore: read the focused workspace: %w", err)
	}
	restored.Focus = domain.Focus{WorkspaceID: workspaceID.String, TabID: tabID.String}

	if err := tx.Commit(); err != nil {
		return ports.Restored{}, fmt.Errorf("treestore: commit restore: %w", err)
	}
	return restored, nil
}

// SetFocus records which workspace is focused and which tab inside it.
//
// Both halves are written together because they are one answer: `session.snapshot` and
// `layout.export` each ask for the pair, and a workspace whose `focused_at` moved without its
// `focused_tab_id` would name a tab from an older visit.
func (s *Store) SetFocus(ctx context.Context, workspaceID, tabID string, atMillis int64) error {
	if workspaceID == "" {
		return nil
	}
	query := `UPDATE workspaces SET focused_at = ? WHERE id = ?`
	args := []any{atMillis, workspaceID}
	if tabID != "" {
		query = `UPDATE workspaces SET focused_at = ?, focused_tab_id = ? WHERE id = ?`
		args = []any{atMillis, tabID, workspaceID}
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("treestore: record focus: %w", err)
	}
	return nil
}

// SetCommandPending records that a pane's stored command has not been run.
func (s *Store) SetCommandPending(ctx context.Context, paneID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE panes SET command_pending = 1 WHERE id = ?`, paneID); err != nil {
		return fmt.Errorf("treestore: mark the command of %s pending: %w", paneID, err)
	}
	return nil
}

// readRestorablePanes reads the open panes of open tabs of open workspaces.
//
// It is its own function so the rows can be closed with `defer` in the scope that owns them,
// which is both what `sqlclosecheck` asks for and the shape that cannot leak a cursor on a
// path someone adds later.
func readRestorablePanes(ctx context.Context, tx *sql.Tx) ([]domain.Pane, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT `+paneColumns+`
		  FROM panes p
		  JOIN tabs t ON t.id = p.tab_id
		  JOIN workspaces w ON w.id = t.workspace_id
		 WHERE p.closed_at IS NULL AND t.closed_at IS NULL AND w.closed_at IS NULL
		 ORDER BY t.workspace_id, p.tab_id, p.order_index`)
	if err != nil {
		return nil, fmt.Errorf("treestore: read the panes to restore: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Pane
	for rows.Next() {
		pane, err := scanPane(rows)
		if err != nil {
			return nil, err
		}
		// The session named here died with the previous daemon; the caller attaches a
		// fresh one. Reporting it empty keeps the service from ever handing a dead id to
		// the sessions module.
		pane.SessionID = ""
		out = append(out, pane)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("treestore: read the panes to restore: %w", err)
	}
	return out, nil
}
