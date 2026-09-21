package treestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
	"github.com/ecrespo/umbral/internal/workspaces/ports"
)

// --- tabs ----------------------------------------------------------------------

// CreateTab adds a tab and its root pane to an open workspace (API Spec §5.5). Like
// `workspace.create`, the two arrive together: a tab with no pane is not something a client
// can display.
func (s *Store) CreateTab(ctx context.Context, workspaceID, label string) (domain.Tab, domain.Pane, error) {
	var (
		tab  domain.Tab
		pane domain.Pane
	)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		tab, pane, err = s.createTabTx(ctx, tx, workspaceID, label)
		return err
	})
	return tab, pane, err
}

// createTabTx is the body, separated for the same reason createWorkspaceTx is: `pane.move`
// with `destination: {"type": "new_tab"}` has to create the tab inside the transaction that
// moves the pane into it, or a failed move leaves an empty tab behind.
func (s *Store) createTabTx(ctx context.Context, tx *sql.Tx, workspaceID, label string) (domain.Tab, domain.Pane, error) {
	var open int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM workspaces WHERE id = ? AND closed_at IS NULL`,
		workspaceID).Scan(&open); err != nil {
		return domain.Tab{}, domain.Pane{}, fmt.Errorf("treestore: create tab: %w", err)
	}
	if open == 0 {
		return domain.Tab{}, domain.Pane{}, fmt.Errorf("%w: workspace %q", domain.ErrNotFound, workspaceID)
	}

	var cwd string
	if err := tx.QueryRowContext(ctx,
		`SELECT cwd FROM workspaces WHERE id = ?`, workspaceID).Scan(&cwd); err != nil {
		return domain.Tab{}, domain.Pane{}, fmt.Errorf("treestore: read workspace cwd: %w", err)
	}

	tabN, err := nextTabOrdinal(ctx, tx, workspaceID)
	if err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}
	paneN, err := nextPaneOrdinal(ctx, tx, workspaceID)
	if err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}

	now := s.millis()
	tabID := domain.FormatTabID(workspaceID, tabN)
	paneID := domain.FormatPaneID(workspaceID, paneN)

	root := domain.PaneNode(domain.Pane{ID: paneID, CWD: cwd})
	raw, err := domain.MarshalLayout(domain.Layout{
		WorkspaceID: workspaceID, TabID: tabID, FocusedPaneID: paneID, Root: root,
	})
	if err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tabs(id, workspace_id, label, order_index, layout_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tabID, workspaceID, label, tabN-1, string(raw), now); err != nil {
		return domain.Tab{}, domain.Pane{}, fmt.Errorf("treestore: insert tab: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO panes(id, tab_id, cwd, env_json, order_index, created_at)
		VALUES (?, ?, ?, '{}', 0, ?)`,
		paneID, tabID, cwd, now); err != nil {
		return domain.Tab{}, domain.Pane{}, fmt.Errorf("treestore: insert pane: %w", err)
	}

	created := epoch(now)
	tab := domain.Tab{
		ID: tabID, WorkspaceID: workspaceID, Label: label, OrderIndex: tabN - 1,
		FocusedPaneID: paneID, CreatedAt: created,
	}
	pane := domain.Pane{
		ID: paneID, TabID: tabID, WorkspaceID: workspaceID, CWD: cwd,
		Env: map[string]string{}, Aliases: []string{paneID},
		AttentionState: domain.AttentionUnknown, CreatedAt: created,
	}
	return tab, pane, nil
}

// ListTabs reports a workspace's open tabs in order.
func (s *Store) ListTabs(ctx context.Context, workspaceID string) ([]domain.Tab, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, label, order_index, layout_json, created_at, closed_at
		  FROM tabs WHERE workspace_id = ? AND closed_at IS NULL ORDER BY order_index, id`,
		workspaceID)
	if err != nil {
		return nil, fmt.Errorf("treestore: list tabs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Tab
	for rows.Next() {
		tab, err := scanTab(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tab)
	}
	return out, rows.Err()
}

func scanTab(row scanner) (domain.Tab, error) {
	var (
		tab      domain.Tab
		raw      string
		created  int64
		closedAt sql.NullInt64
	)
	if err := row.Scan(&tab.ID, &tab.WorkspaceID, &tab.Label, &tab.OrderIndex,
		&raw, &created, &closedAt); err != nil {
		return domain.Tab{}, fmt.Errorf("treestore: read tab: %w", err)
	}
	layout, err := domain.UnmarshalLayout([]byte(raw))
	if err != nil {
		return domain.Tab{}, err
	}
	tab.FocusedPaneID = layout.FocusedPaneID
	tab.CreatedAt = epoch(created)
	tab.ClosedAt = epochPtr(closedAt)
	return tab, nil
}

// GetTab reports one open tab.
func (s *Store) GetTab(ctx context.Context, id string) (domain.Tab, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, label, order_index, layout_json, created_at, closed_at
		  FROM tabs WHERE id = ? AND closed_at IS NULL`, id)
	tab, err := scanTab(row)
	if isNoRows(err) {
		return domain.Tab{}, fmt.Errorf("%w: tab %q", domain.ErrNotFound, id)
	}
	return tab, err
}

// SetTabLabel renames a tab.
func (s *Store) SetTabLabel(ctx context.Context, id, label string) (domain.Tab, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tabs SET label = ? WHERE id = ? AND closed_at IS NULL`, label, id)
	if err != nil {
		return domain.Tab{}, fmt.Errorf("treestore: rename tab: %w", err)
	}
	if err := mustAffect(res, domain.ErrNotFound, "tab", id); err != nil {
		return domain.Tab{}, err
	}
	return s.GetTab(ctx, id)
}

// SetFocusedPane records a tab's focused pane inside its layout, which is where the field
// lives (Data Model §2.4b gives `tabs` no column for it).
func (s *Store) SetFocusedPane(ctx context.Context, tabID, paneID string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		layout, err := readLayout(ctx, tx, tabID)
		if err != nil {
			return err
		}
		// Focusing a pane that is not in this tab would leave a tab pointing at a pane a
		// client cannot draw, which is worse than refusing.
		if !containsPane(layout.Root, paneID) {
			return fmt.Errorf("%w: pane %q is not in tab %q", domain.ErrValidation, paneID, tabID)
		}
		layout.FocusedPaneID = paneID
		return writeLayout(ctx, tx, tabID, layout)
	})
}

func containsPane(root *domain.Node, paneID string) bool {
	for _, id := range root.PaneIDs() {
		if id == paneID {
			return true
		}
	}
	return false
}

// CloseTab closes a tab and the panes under it, reporting their sessions.
func (s *Store) CloseTab(ctx context.Context, id string) ([]string, error) {
	var sessions []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var open int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM tabs WHERE id = ? AND closed_at IS NULL`, id).Scan(&open); err != nil {
			return fmt.Errorf("treestore: close tab: %w", err)
		}
		if open == 0 {
			return fmt.Errorf("%w: tab %q", domain.ErrNotFound, id)
		}

		var err error
		sessions, err = sessionsUnder(ctx, tx,
			`SELECT session_id FROM panes
			  WHERE tab_id = ? AND closed_at IS NULL AND session_id IS NOT NULL`, id)
		if err != nil {
			return err
		}

		now := s.millis()
		if _, err := tx.ExecContext(ctx,
			`UPDATE panes SET closed_at = ?, session_id = NULL WHERE tab_id = ? AND closed_at IS NULL`,
			now, id); err != nil {
			return fmt.Errorf("treestore: close tab panes: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tabs SET closed_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("treestore: close tab: %w", err)
		}
		// Every pane of the tab closed with it, so every screen goes too (Data Model §4).
		return forgetScreens(ctx, tx, forgetScreensOfTab, id)
	})
	return sessions, err
}

// --- layout ---------------------------------------------------------------------

func readLayout(ctx context.Context, tx *sql.Tx, tabID string) (domain.Layout, error) {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT layout_json FROM tabs WHERE id = ? AND closed_at IS NULL`, tabID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Layout{}, fmt.Errorf("%w: tab %q", domain.ErrNotFound, tabID)
	}
	if err != nil {
		return domain.Layout{}, fmt.Errorf("treestore: read layout: %w", err)
	}
	return domain.UnmarshalLayout([]byte(raw))
}

func writeLayout(ctx context.Context, tx *sql.Tx, tabID string, layout domain.Layout) error {
	raw, err := domain.MarshalLayout(layout)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tabs SET layout_json = ? WHERE id = ?`, string(raw), tabID); err != nil {
		return fmt.Errorf("treestore: write layout: %w", err)
	}
	return nil
}

// Layout reports a tab's stored tree.
//
// One statement, so no transaction: with `_txlock=immediate` every transaction takes the
// write lock, and a read that queued behind writers for no reason would be a self-inflicted
// contention.
func (s *Store) Layout(ctx context.Context, tabID string) (domain.Layout, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT layout_json FROM tabs WHERE id = ? AND closed_at IS NULL`, tabID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Layout{}, fmt.Errorf("%w: tab %q", domain.ErrNotFound, tabID)
	}
	if err != nil {
		return domain.Layout{}, fmt.Errorf("treestore: read layout: %w", err)
	}
	return domain.UnmarshalLayout([]byte(raw))
}

// --- panes -----------------------------------------------------------------------

// CreatePane inserts a pane and rewrites its tab's layout in one transaction, so a pane
// never exists without a position and a position never names a pane that does not exist.
func (s *Store) CreatePane(ctx context.Context, tabID string, params domain.SplitParams, root *domain.Node) (domain.Pane, error) {
	var pane domain.Pane
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var workspaceID string
		err := tx.QueryRowContext(ctx,
			`SELECT workspace_id FROM tabs WHERE id = ? AND closed_at IS NULL`, tabID).Scan(&workspaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: tab %q", domain.ErrNotFound, tabID)
		}
		if err != nil {
			return fmt.Errorf("treestore: create pane: %w", err)
		}

		n, err := nextPaneOrdinal(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		paneID := domain.FormatPaneID(workspaceID, n)

		// The caller built the tree around a placeholder, because it could not know the
		// identifier before this transaction allocated it. Naming the leaf is the last
		// step of the same write.
		named, err := root.Rename(placeholderPaneID, "")
		if err != nil && !errors.Is(err, domain.ErrPaneNotInLayout) {
			return err
		}
		if err == nil {
			root = named
		}
		root = renamePaneID(root, placeholderPaneID, paneID, params)

		env, err := jsonEnv(params.Env)
		if err != nil {
			return err
		}
		command, err := jsonCommand(params.Command)
		if err != nil {
			return err
		}

		var order int
		if err := tx.QueryRowContext(ctx,
			`SELECT coalesce(max(order_index), -1) + 1 FROM panes WHERE tab_id = ?`,
			tabID).Scan(&order); err != nil {
			return fmt.Errorf("treestore: order a pane: %w", err)
		}

		now := s.millis()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO panes(id, tab_id, cwd, command_json, env_json, order_index, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			paneID, tabID, params.CWD, command, env, order, now); err != nil {
			return fmt.Errorf("treestore: insert pane: %w", err)
		}

		layout, err := readLayout(ctx, tx, tabID)
		if err != nil {
			return err
		}
		layout.Root = root
		if params.Focus {
			layout.FocusedPaneID = paneID
		}
		if err := writeLayout(ctx, tx, tabID, layout); err != nil {
			return err
		}

		pane = domain.Pane{
			ID: paneID, TabID: tabID, WorkspaceID: workspaceID, CWD: params.CWD,
			Command: params.Command, Env: params.Env, OrderIndex: order,
			Aliases: []string{paneID}, AttentionState: domain.AttentionUnknown,
			CreatedAt: epoch(now),
		}
		if pane.Env == nil {
			pane.Env = map[string]string{}
		}
		return nil
	})
	return pane, err
}

// placeholderPaneID stands in the layout tree for the pane this transaction is about to
// allocate. The service builds the tree before the identifier exists — allocation has to
// happen inside the transaction that inserts the row, or two concurrent splits would pick
// the same number — so the leaf is filled in here.
const placeholderPaneID = "w0:p0"

func renamePaneID(n *domain.Node, from, to string, params domain.SplitParams) *domain.Node {
	if n == nil {
		return nil
	}
	out := *n
	if out.Type == domain.NodePane && out.PaneID == from {
		out.PaneID = to
		out.CWD = params.CWD
		out.Command = params.Command
		out.Env = params.Env
		return &out
	}
	out.First = renamePaneID(n.First, from, to, params)
	out.Second = renamePaneID(n.Second, from, to, params)
	return &out
}

// AttachSession binds a terminal to a pane. The unique partial index on `panes.session_id`
// is what makes a double attach an error rather than a silently shared terminal.
func (s *Store) AttachSession(ctx context.Context, paneID, sessionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE panes SET session_id = ? WHERE id = ? AND closed_at IS NULL`, sessionID, paneID)
	if err != nil {
		return fmt.Errorf("treestore: attach session: %w", err)
	}
	return mustAffect(res, domain.ErrNotFound, "pane", paneID)
}

const paneColumns = `p.id, p.tab_id, t.workspace_id, p.session_id, p.label, p.cwd,
	p.command_json, p.command_pending, p.env_json, p.order_index, p.created_at, p.closed_at`

func scanPane(row scanner) (domain.Pane, error) {
	var (
		pane     domain.Pane
		session  sql.NullString
		label    sql.NullString
		command  sql.NullString
		env      string
		created  int64
		closedAt sql.NullInt64
	)
	if err := row.Scan(&pane.ID, &pane.TabID, &pane.WorkspaceID, &session, &label, &pane.CWD,
		&command, &pane.CommandPending, &env, &pane.OrderIndex, &created, &closedAt); err != nil {
		return domain.Pane{}, fmt.Errorf("treestore: read pane: %w", err)
	}
	pane.SessionID = session.String
	pane.Label = label.String
	if command.Valid {
		if err := json.Unmarshal([]byte(command.String), &pane.Command); err != nil {
			return domain.Pane{}, fmt.Errorf("treestore: stored pane command is not readable: %w", err)
		}
	}
	pane.Env = map[string]string{}
	if err := json.Unmarshal([]byte(env), &pane.Env); err != nil {
		return domain.Pane{}, fmt.Errorf("treestore: stored pane env is not readable: %w", err)
	}
	pane.CreatedAt = epoch(created)
	pane.ClosedAt = epochPtr(closedAt)
	// F0 has no producer of a pane state: `pane_state_reports` is migration 0005 and
	// threads are F1 (delta `2026-09-pane-attention-state`).
	pane.AttentionState = domain.AttentionUnknown
	return pane, nil
}

// ListPanes reports a tab's open panes, ordered as the layout draws them rather than by
// insertion: a client rendering the list beside the panes would otherwise show two orders.
func (s *Store) ListPanes(ctx context.Context, tabID string) ([]domain.Pane, error) {
	layout, err := s.Layout(ctx, tabID)
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, id := range layout.Root.PaneIDs() {
		order[id] = i
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+paneColumns+`
		  FROM panes p JOIN tabs t ON t.id = p.tab_id
		 WHERE p.tab_id = ? AND p.closed_at IS NULL`, tabID)
	if err != nil {
		return nil, fmt.Errorf("treestore: list panes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Pane
	for rows.Next() {
		pane, err := scanPane(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pane)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("treestore: list panes: %w", err)
	}

	sortByLayout(out, order)
	for i := range out {
		aliases, err := s.aliasesOf(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Aliases = aliases
	}
	return out, nil
}

// sortByLayout is an insertion sort: a tab holds a handful of panes, and the layout order
// map is the only ranking that matters. A pane missing from the layout sorts last rather
// than crashing, because a tree and a table disagreeing is a bug to see, not to panic on.
func sortByLayout(panes []domain.Pane, order map[string]int) {
	rank := func(p domain.Pane) int {
		if i, ok := order[p.ID]; ok {
			return i
		}
		return len(order) + 1
	}
	for i := 1; i < len(panes); i++ {
		for j := i; j > 0 && rank(panes[j]) < rank(panes[j-1]); j-- {
			panes[j], panes[j-1] = panes[j-1], panes[j]
		}
	}
}

// aliasesOf lists a pane's identifiers, oldest first, with the current one last. The
// current id is included so a client can render the whole set without appending it, which
// is what API Spec §4's `"aliases":["w1:p2"]` on an unmoved pane shows.
func (s *Store) aliasesOf(ctx context.Context, paneID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT alias_id FROM pane_aliases WHERE pane_id = ? ORDER BY created_at, alias_id`, paneID)
	if err != nil {
		return nil, fmt.Errorf("treestore: list aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []string{}
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, fmt.Errorf("treestore: read alias: %w", err)
		}
		out = append(out, alias)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("treestore: list aliases: %w", err)
	}
	return append(out, paneID), nil
}

// GetPane reports one open pane, resolving a previous identifier through its alias
// (REQ-WS-007).
func (s *Store) GetPane(ctx context.Context, id string) (domain.Pane, error) {
	pane, err := s.getPaneByID(ctx, id)
	if err == nil {
		return pane, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Pane{}, err
	}

	var current string
	aliasErr := s.db.QueryRowContext(ctx,
		`SELECT pane_id FROM pane_aliases WHERE alias_id = ?`, id).Scan(&current)
	if errors.Is(aliasErr, sql.ErrNoRows) {
		return domain.Pane{}, err
	}
	if aliasErr != nil {
		return domain.Pane{}, fmt.Errorf("treestore: resolve alias: %w", aliasErr)
	}
	return s.getPaneByID(ctx, current)
}

func (s *Store) getPaneByID(ctx context.Context, id string) (domain.Pane, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+paneColumns+`
		  FROM panes p JOIN tabs t ON t.id = p.tab_id
		 WHERE p.id = ? AND p.closed_at IS NULL`, id)
	pane, err := scanPane(row)
	if isNoRows(err) {
		return domain.Pane{}, fmt.Errorf("%w: pane %q", domain.ErrNotFound, id)
	}
	if err != nil {
		return domain.Pane{}, err
	}
	aliases, err := s.aliasesOf(ctx, pane.ID)
	if err != nil {
		return domain.Pane{}, err
	}
	pane.Aliases = aliases
	return pane, nil
}

// SetPaneLabel renames a pane, in the row and in the layout leaf, so an exported tree
// carries the name the user gave it.
func (s *Store) SetPaneLabel(ctx context.Context, id, label string) (domain.Pane, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var tabID string
		err := tx.QueryRowContext(ctx,
			`SELECT tab_id FROM panes WHERE id = ? AND closed_at IS NULL`, id).Scan(&tabID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: pane %q", domain.ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("treestore: rename pane: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE panes SET label = ? WHERE id = ?`, label, id); err != nil {
			return fmt.Errorf("treestore: rename pane: %w", err)
		}

		layout, err := readLayout(ctx, tx, tabID)
		if err != nil {
			return err
		}
		renamed, err := layout.Root.Rename(id, label)
		if err != nil {
			return err
		}
		layout.Root = renamed
		return writeLayout(ctx, tx, tabID, layout)
	})
	if err != nil {
		return domain.Pane{}, err
	}
	return s.GetPane(ctx, id)
}

// ClosePane closes one pane, takes it out of its tab's layout and reports its session.
func (s *Store) ClosePane(ctx context.Context, id string) (string, error) {
	var sessionID string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var (
			tabID   string
			session sql.NullString
		)
		err := tx.QueryRowContext(ctx,
			`SELECT tab_id, session_id FROM panes WHERE id = ? AND closed_at IS NULL`,
			id).Scan(&tabID, &session)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: pane %q", domain.ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("treestore: close pane: %w", err)
		}
		sessionID = session.String

		layout, err := readLayout(ctx, tx, tabID)
		if err != nil {
			return err
		}
		root, err := layout.Root.RemovePane(id)
		if err != nil {
			return err
		}
		layout.Root = root
		if layout.FocusedPaneID == id {
			// Focus falls to whatever is left, or to nothing when the tab is now empty.
			// Leaving it pointing at a closed pane would make the next `tab.focus` draw a
			// pane that is gone.
			remaining := root.PaneIDs()
			layout.FocusedPaneID = ""
			if len(remaining) > 0 {
				layout.FocusedPaneID = remaining[0]
			}
		}
		if err := writeLayout(ctx, tx, tabID, layout); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE panes SET closed_at = ?, session_id = NULL WHERE id = ?`,
			s.millis(), id); err != nil {
			return fmt.Errorf("treestore: close pane: %w", err)
		}
		// Data Model §4: a stored screen is kept "until the pane closes". This is that
		// moment, and nothing else reaches the row — the pane is closed, not deleted, so
		// pane_history's ON DELETE CASCADE never fires.
		return forgetScreens(ctx, tx, forgetScreenOfPane, id)
	})
	return sessionID, err
}

var _ ports.Tree = (*Store)(nil)
