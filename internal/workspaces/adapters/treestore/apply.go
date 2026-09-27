package treestore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// ApplyLayout builds a tab that reproduces a portable tree (REQ-WS-005).
//
// One transaction for the whole tab, because a layout is applied or it is not: a client
// that asked for four panes and got two would have to work out which two, and the tree it
// was drawing from no longer describes what it has.
//
// The incoming tree's pane identifiers are ignored. They belong to whatever workspace
// exported it — possibly on another machine — and reusing them would either collide with a
// live pane or resurrect a name an alias still answers to. Everything else the node carries
// is reproduced: label, cwd, env and command, which is REQ-WS-005's list.
func (s *Store) ApplyLayout(ctx context.Context, params domain.ApplyLayoutParams) (domain.Applied, error) {
	var applied domain.Applied
	err := s.tx(ctx, func(tx *sql.Tx) error {
		tab, placeholder, err := s.createTabTx(ctx, tx, params.WorkspaceID, params.TabLabel)
		if err != nil {
			return err
		}
		// `tab.create` hands back a tab with a root pane, because an empty one is not
		// something a client can draw. The applied tree supplies its own panes, so that
		// one is closed rather than left as an extra nobody asked for.
		if err := closePaneTx(ctx, tx, s.millis(), placeholder.ID); err != nil {
			return err
		}

		var workspaceCWD string
		if err := tx.QueryRowContext(ctx,
			`SELECT cwd FROM workspaces WHERE id = ?`, params.WorkspaceID).Scan(&workspaceCWD); err != nil {
			return fmt.Errorf("treestore: read workspace cwd: %w", err)
		}

		root, panes, err := s.materialise(ctx, tx, params.Root, tab, workspaceCWD)
		if err != nil {
			return err
		}

		layout := domain.Layout{
			WorkspaceID: params.WorkspaceID, TabID: tab.ID, Root: root,
		}
		if len(panes) > 0 {
			layout.FocusedPaneID = panes[0].ID
			tab.FocusedPaneID = panes[0].ID
		}
		if err := writeLayout(ctx, tx, tab.ID, layout); err != nil {
			return err
		}

		applied = domain.Applied{
			Tab: tab, Panes: panes,
			Warnings: []string{domain.ApplyWarning},
		}
		return nil
	})
	return applied, err
}

// materialise walks the incoming tree, inserting a pane row per leaf and returning the same
// shape with this workspace's identifiers in it.
//
// It returns the panes in the order the leaves appear, left to right, which is the order a
// client draws them and the order `pane.list` reports.
func (s *Store) materialise(ctx context.Context, tx *sql.Tx, node *domain.Node, tab domain.Tab, fallbackCWD string) (*domain.Node, []domain.Pane, error) {
	switch node.Type {
	case domain.NodePane:
		pane, err := s.insertPane(ctx, tx, node, tab, fallbackCWD)
		if err != nil {
			return nil, nil, err
		}
		leaf := *node
		leaf.PaneID = pane.ID
		leaf.CWD = pane.CWD
		return &leaf, []domain.Pane{pane}, nil

	case domain.NodeSplit:
		first, firstPanes, err := s.materialise(ctx, tx, node.First, tab, fallbackCWD)
		if err != nil {
			return nil, nil, err
		}
		second, secondPanes, err := s.materialise(ctx, tx, node.Second, tab, fallbackCWD)
		if err != nil {
			return nil, nil, err
		}
		out := *node
		out.First, out.Second = first, second
		out.Ratio = domain.ClampRatio(node.Ratio)
		return &out, append(firstPanes, secondPanes...), nil
	}
	return nil, nil, fmt.Errorf("%w: layout node of unknown type %q", domain.ErrValidation, node.Type)
}

// insertPane writes one pane of an applied layout.
func (s *Store) insertPane(ctx context.Context, tx *sql.Tx, leaf *domain.Node, tab domain.Tab, fallbackCWD string) (domain.Pane, error) {
	n, err := nextPaneOrdinal(ctx, tx, tab.WorkspaceID)
	if err != nil {
		return domain.Pane{}, err
	}
	paneID := domain.FormatPaneID(tab.WorkspaceID, n)

	// A node with no cwd falls back to the workspace's. An exported layout always carries
	// one, but a hand-written tree need not, and starting a shell in the daemon's own
	// directory would be the wrong kind of surprise.
	cwd := leaf.CWD
	if cwd == "" {
		cwd = fallbackCWD
	}

	env, err := jsonEnv(leaf.Env)
	if err != nil {
		return domain.Pane{}, err
	}
	command, err := jsonCommand(leaf.Command)
	if err != nil {
		return domain.Pane{}, err
	}

	var order int
	if err := tx.QueryRowContext(ctx,
		`SELECT coalesce(max(order_index), -1) + 1 FROM panes WHERE tab_id = ?`,
		tab.ID).Scan(&order); err != nil {
		return domain.Pane{}, fmt.Errorf("treestore: order a pane: %w", err)
	}

	now := s.millis()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO panes(id, tab_id, label, cwd, command_json, env_json, order_index, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		paneID, tab.ID, nullLabel(leaf.Label), cwd, command, env, order, now); err != nil {
		return domain.Pane{}, fmt.Errorf("treestore: insert pane: %w", err)
	}

	pane := domain.Pane{
		ID: paneID, TabID: tab.ID, WorkspaceID: tab.WorkspaceID,
		Label: leaf.Label, CWD: cwd, Command: leaf.Command, Env: leaf.Env,
		OrderIndex: order, Aliases: []string{paneID},
		AttentionState: domain.AttentionUnknown, CreatedAt: epoch(now),
	}
	if pane.Env == nil {
		pane.Env = map[string]string{}
	}
	return pane, nil
}

// nullLabel keeps an unnamed pane's label NULL rather than the empty string, which is what
// `panes.label` being nullable is for: "no label" and "a label that is blank" are different
// and only the first is what an exported tree without one means.
func nullLabel(label string) sql.NullString {
	if label == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: label, Valid: true}
}
