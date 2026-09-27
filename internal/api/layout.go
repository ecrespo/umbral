package api

import (
	"context"
	"encoding/json"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// `layout.export` and `layout.apply` (API Spec §5.7 and §5.8).
//
// They are in their own file, and their own §2 capability, because they are the one part of
// the tree a client takes away with it: an exported layout outlives the daemon that made
// it, and is the thing a person checks into a repository beside the project it describes.

// layoutExportParams is §5.7's request. `tab_id` is optional and defaults to the focused
// tab, which is why a client can bind this to a key without tracking which tab it is on.
type layoutExportParams struct {
	TabID string `json:"tab_id" api:"optional"`
}

// handleLayoutExport returns a tab's tree (REQ-WS-004).
//
// `tab_id` is optional and defaults to the focused tab (§5.7). The default is the reason a
// client can bind this to a key without tracking which tab it is on.
func handleLayoutExport(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params layoutExportParams
	if err := decode(raw, &params, "layout.export"); err != nil {
		return nil, err
	}
	layout, err := tree.ExportLayout(ctx, params.TabID)
	if err != nil {
		return nil, err
	}
	return toWireLayout(layout), nil
}

// applyParams is §5.8's request. `root` is decoded into the domain's own node type, which
// is the one place a client's tree crosses into the module — and why `Validate` runs on it
// before anything is created.
type applyParams struct {
	WorkspaceID string         `json:"workspace_id"`
	TabLabel    string         `json:"tab_label" api:"optional"`
	Root        *wsdomain.Node `json:"root" api:"required"`
	Focus       *bool          `json:"focus"`
}

// handleLayoutApply creates a tab from a portable tree (REQ-WS-005).
//
// The response carries the warnings the requirement demands, and it carries them as data
// rather than as prose in a comment: REQ-WS-005 says the system "SHALL state in the
// response that live processes and scrollback are not reproduced", so a client can show the
// user the same sentence every time instead of inventing one.
func handleLayoutApply(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params applyParams
	if err := decode(raw, &params, "layout.apply"); err != nil {
		return nil, err
	}

	applied, err := tree.ApplyLayout(ctx, wsdomain.ApplyLayoutParams{
		WorkspaceID: params.WorkspaceID,
		TabLabel:    params.TabLabel,
		Root:        params.Root,
		Focus:       optionalBool(params.Focus, true),
	})
	if err != nil {
		return nil, err
	}

	panes := make([]Pane, 0, len(applied.Panes))
	for _, pane := range applied.Panes {
		panes = append(panes, toWirePane(pane))
	}
	return layoutApplyResult{
		Tab:      toWireTab(applied.Tab),
		Panes:    panes,
		Warnings: applied.Warnings,
	}, nil
}
