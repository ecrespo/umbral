package treestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
	"github.com/ecrespo/umbral/internal/workspaces/ports"
)

// MovePane is REQ-WS-007: the pane keeps its terminal and its process, gets a new public
// identifier, and the old one stays resolvable while that terminal lives.
//
// The whole thing is one transaction because it touches four things — the pane row, two
// tabs' layouts and the alias table — and any half of it is a broken tree: a pane in no
// layout, a layout naming a pane that moved, or an alias pointing at an identifier that no
// longer exists.
//
// Only a move that crosses a workspace renames the pane. That falls out of REQ-WS-002's
// grammar rather than being a separate rule: `w<n>:p<m>` names a workspace and a pane and
// says nothing about a tab, so dragging a pane between two tabs of one workspace cannot
// change its name, and REQ-WS-007's "moved to another workspace" is exactly the case that
// can.
func (s *Store) MovePane(ctx context.Context, params domain.MoveParams) (ports.Moved, error) {
	var moved ports.Moved
	err := s.tx(ctx, func(tx *sql.Tx) error {
		sourceTab, sourceWorkspace, err := paneLocation(ctx, tx, params.PaneID)
		if err != nil {
			return err
		}

		pane, err := paneInTx(ctx, tx, params.PaneID)
		if err != nil {
			return err
		}
		dest, err := s.resolveDestination(ctx, tx, params.Destination, sourceWorkspace, pane.CWD)
		if err != nil {
			return err
		}
		destTab, destWorkspace := dest.tabID, dest.workspaceID
		if destTab == sourceTab {
			return fmt.Errorf("%w: pane %q is already in tab %q",
				domain.ErrConflict, params.PaneID, destTab)
		}

		// Take the pane out of the tree it is leaving before anything is renamed, so the
		// removal still finds it under the identifier that tree knows.
		sourceLayout, err := readLayout(ctx, tx, sourceTab)
		if err != nil {
			return err
		}
		leaf := findPane(sourceLayout.Root, params.PaneID)
		if leaf == nil {
			return fmt.Errorf("%w: pane %q is not in the layout of tab %q",
				domain.ErrNotFound, params.PaneID, sourceTab)
		}
		trimmed, err := sourceLayout.Root.RemovePane(params.PaneID)
		if err != nil {
			return err
		}
		sourceLayout.Root = trimmed
		if sourceLayout.FocusedPaneID == params.PaneID {
			sourceLayout.FocusedPaneID = ""
			if remaining := trimmed.PaneIDs(); len(remaining) > 0 {
				sourceLayout.FocusedPaneID = remaining[0]
			}
		}
		if err := writeLayout(ctx, tx, sourceTab, sourceLayout); err != nil {
			return err
		}

		newID := params.PaneID
		if destWorkspace != sourceWorkspace {
			newID, err = s.rename(ctx, tx, params.PaneID, destWorkspace)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE panes SET tab_id = ? WHERE id = ?`, destTab, newID); err != nil {
			return fmt.Errorf("treestore: move pane: %w", err)
		}

		// Where a moved pane lands is not written down anywhere: API Spec §5.6 says the
		// destination and says nothing about the position inside it. It goes beside the
		// destination's focused pane, split to the right at the default ratio, which is
		// where a pane dragged onto a tab would be expected to appear; an empty tab takes
		// it as the root.
		destLayout, err := readLayout(ctx, tx, destTab)
		if err != nil {
			return err
		}
		arriving := *leaf
		arriving.PaneID = newID
		if destLayout.Root == nil {
			destLayout.Root = &arriving
		} else {
			anchor := destLayout.FocusedPaneID
			if anchor == "" {
				ids := destLayout.Root.PaneIDs()
				anchor = ids[len(ids)-1]
			}
			grown, err := destLayout.Root.SplitAt(anchor, domain.SplitRight, domain.DefaultSplitRatio, &arriving)
			if err != nil {
				return err
			}
			destLayout.Root = grown
		}
		destLayout.FocusedPaneID = newID
		destLayout.TabID = destTab
		destLayout.WorkspaceID = destWorkspace
		if err := writeLayout(ctx, tx, destTab, destLayout); err != nil {
			return err
		}

		sourceLayout.TabID = sourceTab
		sourceLayout.WorkspaceID = sourceWorkspace
		moved = ports.Moved{
			PreviousPaneID:      params.PaneID,
			PreviousWorkspaceID: sourceWorkspace,
			Layout:              destLayout,
			SourceLayout:        sourceLayout,
			CreatedWorkspace:    dest.createdWorkspace,
			CreatedTab:          dest.createdTab,
		}
		moved.Pane, err = paneInTx(ctx, tx, newID)
		return err
	})
	return moved, err
}

// rename gives the pane a fresh identifier in its new workspace and records every
// identifier it has answered to as an alias of the new one.
//
// The aliases are deleted and rewritten rather than updated in place because
// `pane_aliases.pane_id` is a foreign key on `panes(id)` with no ON UPDATE clause: with
// foreign keys on, renaming a pane that anything still references fails. Clearing the
// references first, moving the parent, then restoring them is what that constraint leaves
// available — and it is also what keeps the alias chain flat, so a pane moved three times
// answers to all four of its names directly rather than through a chain of lookups.
func (s *Store) rename(ctx context.Context, tx *sql.Tx, paneID, destWorkspace string) (string, error) {
	n, err := nextPaneOrdinal(ctx, tx, destWorkspace)
	if err != nil {
		return "", err
	}
	newID := domain.FormatPaneID(destWorkspace, n)

	previous, err := aliasRows(ctx, tx, paneID)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pane_aliases WHERE pane_id = ?`, paneID); err != nil {
		return "", fmt.Errorf("treestore: clear aliases: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE panes SET id = ? WHERE id = ?`, newID, paneID); err != nil {
		return "", fmt.Errorf("treestore: rename pane: %w", err)
	}

	now := s.millis()
	for _, alias := range append(previous, paneID) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO pane_aliases(alias_id, pane_id, created_at) VALUES (?, ?, ?)`,
			alias, newID, now); err != nil {
			return "", fmt.Errorf("treestore: record alias %q: %w", alias, err)
		}
	}
	return newID, nil
}

func aliasRows(ctx context.Context, tx *sql.Tx, paneID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT alias_id FROM pane_aliases WHERE pane_id = ? ORDER BY created_at, alias_id`, paneID)
	if err != nil {
		return nil, fmt.Errorf("treestore: read aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, fmt.Errorf("treestore: read alias: %w", err)
		}
		out = append(out, alias)
	}
	return out, rows.Err()
}

func paneLocation(ctx context.Context, tx *sql.Tx, paneID string) (tabID, workspaceID string, err error) {
	err = tx.QueryRowContext(ctx, `
		SELECT p.tab_id, t.workspace_id FROM panes p JOIN tabs t ON t.id = p.tab_id
		 WHERE p.id = ? AND p.closed_at IS NULL`, paneID).Scan(&tabID, &workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: pane %q", domain.ErrNotFound, paneID)
	}
	if err != nil {
		return "", "", fmt.Errorf("treestore: locate pane: %w", err)
	}
	return tabID, workspaceID, nil
}

func paneInTx(ctx context.Context, tx *sql.Tx, id string) (domain.Pane, error) {
	row := tx.QueryRowContext(ctx, `
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
	aliases, err := aliasRows(ctx, tx, id)
	if err != nil {
		return domain.Pane{}, err
	}
	pane.Aliases = append(aliases, id)
	return pane, nil
}

// destination is where a move is going, plus whatever had to be built to get there.
type destination struct {
	tabID            string
	workspaceID      string
	createdWorkspace *domain.Workspace
	createdTab       *domain.Tab
}

// resolveDestination turns API Spec §5.6's destination into a tab that exists, creating one
// — and a workspace with it — when the destination asks for that.
func (s *Store) resolveDestination(ctx context.Context, tx *sql.Tx, dest domain.MoveDestination, sourceWorkspace, paneCWD string) (destination, error) {
	switch dest.Type {
	case domain.MoveToTab:
		var open int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM tabs WHERE id = ? AND closed_at IS NULL`, dest.TabID).Scan(&open); err != nil {
			return destination{}, fmt.Errorf("treestore: resolve destination: %w", err)
		}
		if open == 0 {
			return destination{}, fmt.Errorf("%w: tab %q", domain.ErrNotFound, dest.TabID)
		}
		ws, _, ok := domain.ParseTabID(dest.TabID)
		if !ok {
			return destination{}, fmt.Errorf("%w: %q is not a tab identifier", domain.ErrValidation, dest.TabID)
		}
		return destination{tabID: dest.TabID, workspaceID: ws}, nil

	case domain.MoveToNewTab:
		target := dest.WorkspaceID
		if target == "" {
			target = sourceWorkspace
		}
		tab, pane, err := s.createTabTx(ctx, tx, target, dest.Label)
		if err != nil {
			return destination{}, err
		}
		// A new tab arrives with a root pane it does not need: the pane being moved is
		// about to take that place. Closing it here keeps `tab.create`'s invariant — a tab
		// is never returned empty — without leaving an unused terminal behind, and it has
		// no session yet because nothing has attached one.
		if err := closePaneTx(ctx, tx, s.millis(), pane.ID); err != nil {
			return destination{}, err
		}
		if err := writeLayout(ctx, tx, tab.ID, domain.Layout{
			WorkspaceID: target, TabID: tab.ID,
		}); err != nil {
			return destination{}, err
		}
		return destination{tabID: tab.ID, workspaceID: target, createdTab: &tab}, nil

	case domain.MoveToNewWorkspace:
		// The new workspace inherits the moved pane's working directory. §5.4 makes `cwd`
		// required for a workspace a client creates, and a workspace created as a side
		// effect of a move has no caller to ask — the pane arriving in it is the only
		// thing that knows where it belongs.
		tree, err := s.createWorkspaceTx(ctx, tx, domain.CreateWorkspaceParams{
			Label: dest.Label, CWD: paneCWD, TabLabel: dest.Label,
		})
		if err != nil {
			return destination{}, err
		}
		if err := closePaneTx(ctx, tx, s.millis(), tree.RootPane.ID); err != nil {
			return destination{}, err
		}
		if err := writeLayout(ctx, tx, tree.Tab.ID, domain.Layout{
			WorkspaceID: tree.Workspace.ID, TabID: tree.Tab.ID,
		}); err != nil {
			return destination{}, err
		}
		return destination{
			tabID: tree.Tab.ID, workspaceID: tree.Workspace.ID,
			createdWorkspace: &tree.Workspace, createdTab: &tree.Tab,
		}, nil
	}
	return destination{}, fmt.Errorf("%w: destination type %q", domain.ErrValidation, dest.Type)
}

func closePaneTx(ctx context.Context, tx *sql.Tx, now int64, paneID string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE panes SET closed_at = ?, session_id = NULL WHERE id = ?`, now, paneID); err != nil {
		return fmt.Errorf("treestore: close the placeholder pane: %w", err)
	}
	return nil
}

// findPane returns the leaf for a pane, so a move can carry its label, cwd, command and env
// into the destination tree instead of rebuilding them from the row.
func findPane(n *domain.Node, paneID string) *domain.Node {
	if n == nil {
		return nil
	}
	if n.Type == domain.NodePane {
		if n.PaneID == paneID {
			return n
		}
		return nil
	}
	if found := findPane(n.First, paneID); found != nil {
		return found
	}
	return findPane(n.Second, paneID)
}
