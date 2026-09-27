// Package treestore persists the workspace tree in SQLite (Data Model §2.4b).
//
// It is an adapter, not part of the store package, for the same reason blockstore is:
// `store` owns migration 0003 and the shape of the tables; this owns what the rows mean.
//
// Every exported method is one transaction. That is not caution, it is the contract: a
// split writes a pane row and rewrites the tab's layout, and a move writes two layouts, a
// renamed pane and an alias. A tree that lost half of any of those would have a pane with
// no position, or a position naming a pane that does not exist, and nothing in the schema
// would notice.
package treestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
	"github.com/ecrespo/umbral/internal/workspaces/ports"
)

// Store is the tree's persistence.
type Store struct {
	db *sql.DB
	// now is the clock, injectable so tests can pin created_at without sleeping.
	now func() time.Time
}

// New builds a tree store over an open database.
func New(st *store.Store) (*Store, error) {
	if st == nil {
		return nil, fmt.Errorf("treestore: a Store is required")
	}
	return &Store{db: st.DB(), now: time.Now}, nil
}

// tx runs fn inside a transaction, rolling back on any error.
//
// The rollback is deferred unconditionally and the commit sets the error: a Rollback after
// a successful Commit is a no-op that returns sql.ErrTxDone, which is why it is discarded
// rather than reported.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("treestore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("treestore: commit: %w", err)
	}
	return nil
}

func (s *Store) millis() int64 { return s.now().UTC().UnixMilli() }

// epoch converts the column's UTC epoch milliseconds (Art. 6) back to a time.
func epoch(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func epochPtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := epoch(ms.Int64)
	return &t
}

// --- identifier allocation ---------------------------------------------------
//
// REQ-WS-002 requires identifiers unique and stable while the object exists, and the Art. 6
// amendment adds that one is never reused while its object lives. Allocation is therefore
// "one past the highest number this scope has ever handed out", computed inside the same
// transaction as the insert so two concurrent creates cannot pick the same number.
//
// Closed rows are kept for 30 days (Data Model §7) rather than deleted, so a closed
// workspace's number stays taken for as long as anything could still refer to it.

// nextWorkspaceOrdinal returns the number for a new workspace.
//
// The arithmetic is done in SQL rather than by parsing in Go because the alternative is
// reading every row to find a maximum. `substr(id, 2)` drops the leading `w`, and SQLite's
// `CAST(... AS INTEGER)` stops at the first non-digit, which is safe here because the CHECK
// constraint already guarantees the rest is digits.
func nextWorkspaceOrdinal(ctx context.Context, tx *sql.Tx) (int, error) {
	var highest sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT max(CAST(substr(id, 2) AS INTEGER)) FROM workspaces`).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("treestore: allocate a workspace id: %w", err)
	}
	return int(highest.Int64) + 1, nil
}

// nextTabOrdinal returns the number for a new tab of one workspace. `instr(id, ':t')` finds
// the separator, so the substring starts after it.
func nextTabOrdinal(ctx context.Context, tx *sql.Tx, workspaceID string) (int, error) {
	var highest sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT max(CAST(substr(id, instr(id, ':t') + 2) AS INTEGER))
		 FROM tabs WHERE workspace_id = ?`, workspaceID).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("treestore: allocate a tab id: %w", err)
	}
	return int(highest.Int64) + 1, nil
}

// nextPaneOrdinal returns the number for a new pane of one workspace.
//
// It is the only allocator that has to look in two places. A moved pane leaves its old
// identifier behind in `pane_aliases`, and that alias stays resolvable for the life of the
// terminal (REQ-WS-007); handing the same number to a new pane would make one string name
// two panes, and `GetPane` would answer with whichever table it happened to read first.
// Nothing in the schema forbids it — the alias and the pane live in different tables — so
// the allocator is where it has to be forbidden.
func nextPaneOrdinal(ctx context.Context, tx *sql.Tx, workspaceID string) (int, error) {
	var highest sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT max(n) FROM (
			SELECT CAST(substr(p.id, instr(p.id, ':p') + 2) AS INTEGER) AS n
			  FROM panes p
			  JOIN tabs t ON t.id = p.tab_id
			 WHERE t.workspace_id = ?
			UNION ALL
			SELECT CAST(substr(a.alias_id, instr(a.alias_id, ':p') + 2) AS INTEGER) AS n
			  FROM pane_aliases a
			 WHERE a.alias_id GLOB ? || ':p*'
		)`, workspaceID, workspaceID).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("treestore: allocate a pane id: %w", err)
	}
	return int(highest.Int64) + 1, nil
}

// --- workspaces ---------------------------------------------------------------

// CreateWorkspace writes the workspace, its first tab and its root pane in one transaction
// (REQ-WS-001). Either a client gets all three or it gets an error; a tree with a workspace
// and no tab is not a state any caller can do anything with.
func (s *Store) CreateWorkspace(ctx context.Context, params domain.CreateWorkspaceParams) (domain.Tree, error) {
	var tree domain.Tree
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		tree, err = s.createWorkspaceTx(ctx, tx, params)
		return err
	})
	return tree, err
}

// createWorkspaceTx is the body, separated so `pane.move` can build a workspace inside the
// transaction that moves the pane into it. A move to a new workspace that committed the
// workspace and then failed the move would leave an empty workspace nobody asked for.
func (s *Store) createWorkspaceTx(ctx context.Context, tx *sql.Tx, params domain.CreateWorkspaceParams) (domain.Tree, error) {
	n, err := nextWorkspaceOrdinal(ctx, tx)
	if err != nil {
		return domain.Tree{}, err
	}
	now := s.millis()
	wsID := domain.FormatWorkspaceID(n)
	tabID := domain.FormatTabID(wsID, 1)
	paneID := domain.FormatPaneID(wsID, 1)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspaces(id, label, cwd, order_index, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		wsID, params.Label, params.CWD, n-1, now); err != nil {
		return domain.Tree{}, fmt.Errorf("treestore: insert workspace: %w", err)
	}

	root := domain.PaneNode(domain.Pane{ID: paneID, CWD: params.CWD})
	layout, err := domain.MarshalLayout(domain.Layout{
		WorkspaceID: wsID, TabID: tabID, FocusedPaneID: paneID, Root: root,
	})
	if err != nil {
		return domain.Tree{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tabs(id, workspace_id, label, order_index, layout_json, created_at)
		VALUES (?, ?, ?, 0, ?, ?)`,
		tabID, wsID, params.TabLabel, string(layout), now); err != nil {
		return domain.Tree{}, fmt.Errorf("treestore: insert tab: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO panes(id, tab_id, cwd, env_json, order_index, created_at)
		VALUES (?, ?, ?, '{}', 0, ?)`,
		paneID, tabID, params.CWD, now); err != nil {
		return domain.Tree{}, fmt.Errorf("treestore: insert pane: %w", err)
	}

	created := epoch(now)
	return domain.Tree{
		Workspace: domain.Workspace{
			ID: wsID, Label: params.Label, CWD: params.CWD, OrderIndex: n - 1,
			RollupState: domain.AttentionUnknown, TabIDs: []string{tabID},
			CreatedAt: created,
		},
		Tab: domain.Tab{
			ID: tabID, WorkspaceID: wsID, Label: params.TabLabel,
			FocusedPaneID: paneID, CreatedAt: created,
		},
		RootPane: domain.Pane{
			ID: paneID, TabID: tabID, WorkspaceID: wsID, CWD: params.CWD,
			Env: map[string]string{}, Aliases: []string{paneID},
			AttentionState: domain.AttentionUnknown, CreatedAt: created,
		},
	}, nil
}

// ListWorkspaces reports the open workspaces with their tab identifiers.
//
// The rollup is not computed here. It is REQ-WS-006's rule over pane and thread states, and
// the rule belongs in domain where it is tested; the service asks for the children and
// applies it.
func (s *Store) ListWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, label, cwd, order_index, created_at, closed_at
		  FROM workspaces WHERE closed_at IS NULL ORDER BY order_index, id`)
	if err != nil {
		return nil, fmt.Errorf("treestore: list workspaces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Workspace
	for rows.Next() {
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("treestore: list workspaces: %w", err)
	}
	for i := range out {
		ids, err := s.tabIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].TabIDs = ids
	}
	return out, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanWorkspace(row scanner) (domain.Workspace, error) {
	var (
		ws       domain.Workspace
		created  int64
		closedAt sql.NullInt64
	)
	if err := row.Scan(&ws.ID, &ws.Label, &ws.CWD, &ws.OrderIndex, &created, &closedAt); err != nil {
		return domain.Workspace{}, fmt.Errorf("treestore: read workspace: %w", err)
	}
	ws.CreatedAt = epoch(created)
	ws.ClosedAt = epochPtr(closedAt)
	ws.RollupState = domain.AttentionUnknown
	return ws, nil
}

func (s *Store) tabIDs(ctx context.Context, workspaceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM tabs WHERE workspace_id = ? AND closed_at IS NULL ORDER BY order_index, id`,
		workspaceID)
	if err != nil {
		return nil, fmt.Errorf("treestore: list tab ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("treestore: read tab id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetWorkspace reports one open workspace.
func (s *Store) GetWorkspace(ctx context.Context, id string) (domain.Workspace, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, label, cwd, order_index, created_at, closed_at
		  FROM workspaces WHERE id = ? AND closed_at IS NULL`, id)
	ws, err := scanWorkspace(row)
	if errors.Is(err, sql.ErrNoRows) || isNoRows(err) {
		return domain.Workspace{}, fmt.Errorf("%w: workspace %q", domain.ErrNotFound, id)
	}
	if err != nil {
		return domain.Workspace{}, err
	}
	ids, err := s.tabIDs(ctx, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	ws.TabIDs = ids
	return ws, nil
}

// isNoRows sees through the wrapping scanWorkspace does, so a missing row is reported as
// ErrNotFound rather than as a read failure.
func isNoRows(err error) bool { return err != nil && errors.Is(err, sql.ErrNoRows) }

// SetWorkspaceLabel renames a workspace.
func (s *Store) SetWorkspaceLabel(ctx context.Context, id, label string) (domain.Workspace, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE workspaces SET label = ? WHERE id = ? AND closed_at IS NULL`, label, id)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("treestore: rename workspace: %w", err)
	}
	if err := mustAffect(res, domain.ErrNotFound, "workspace", id); err != nil {
		return domain.Workspace{}, err
	}
	return s.GetWorkspace(ctx, id)
}

// mustAffect turns "the UPDATE matched nothing" into the sentinel the caller expects. An
// update that changed no rows and an update of a row that does not exist are the same
// result from SQLite's side, and only the second is an error worth reporting — but for
// these statements the two coincide, because every one of them also sets a value.
func mustAffect(res sql.Result, sentinel error, kind, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("treestore: %s %q: %w", kind, id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s %q", sentinel, kind, id)
	}
	return nil
}

// CloseWorkspace closes a workspace, its tabs and its panes, and reports the sessions that
// were attached.
//
// The sessions are collected before the rows are marked closed, because after the update
// there is nothing left to read them from — and they are returned rather than terminated
// here, since killing a process is not something a storage adapter should be able to do.
func (s *Store) CloseWorkspace(ctx context.Context, id string) ([]string, error) {
	var sessions []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM workspaces WHERE id = ? AND closed_at IS NULL`, id).Scan(&exists)
		if err != nil {
			return fmt.Errorf("treestore: close workspace: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("%w: workspace %q", domain.ErrNotFound, id)
		}

		sessions, err = sessionsUnder(ctx, tx,
			`SELECT p.session_id FROM panes p JOIN tabs t ON t.id = p.tab_id
			  WHERE t.workspace_id = ? AND p.closed_at IS NULL AND p.session_id IS NOT NULL`, id)
		if err != nil {
			return err
		}

		now := s.millis()
		for _, stmt := range []string{
			`UPDATE panes SET closed_at = ?, session_id = NULL
			  WHERE closed_at IS NULL AND tab_id IN (SELECT id FROM tabs WHERE workspace_id = ?)`,
			`UPDATE tabs SET closed_at = ? WHERE closed_at IS NULL AND workspace_id = ?`,
			`UPDATE workspaces SET closed_at = ? WHERE id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, stmt, now, id); err != nil {
				return fmt.Errorf("treestore: close workspace: %w", err)
			}
		}
		// Every pane under the workspace closed with it, so every screen goes too
		// (Data Model §4).
		return forgetScreens(ctx, tx, forgetScreensOfWorkspace, id)
	})
	return sessions, err
}

func sessionsUnder(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("treestore: collect sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("treestore: read session id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// jsonEnv encodes a pane's environment. An empty map and a nil map both store `{}`, which
// is what the column defaults to, so a pane written by either path reads back the same.
func jsonEnv(env map[string]string) (string, error) {
	if len(env) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("treestore: encode pane env: %w", err)
	}
	return string(raw), nil
}

// jsonCommand encodes a pane's launch argv, or NULL when it has none: the column is
// nullable on purpose, because "no command" and "an empty command" are different and only
// the first is a pane running a plain shell.
func jsonCommand(command []string) (sql.NullString, error) {
	if len(command) == 0 {
		return sql.NullString{}, nil
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("treestore: encode pane command: %w", err)
	}
	return sql.NullString{String: string(raw), Valid: true}, nil
}

var _ ports.Tree = (*Store)(nil)
