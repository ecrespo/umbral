package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// The workspace tree's wire surface (API Spec §5.4 to §5.6).
//
// Everything here is translation. The rule the module lives under is that it never learns a
// JSON-RPC code: it returns the sentinels in `workspaces/domain`, and `toWire` in
// errors.go turns them into the §5.4 table. What this file adds is the parameter names,
// the field names and the defaults the spec writes down but a Go signature cannot.

// The result keys of API Spec §5.4 to §5.6. They are constants because the same key
// appears in several results, and a typo in one of them would be a field a client silently
// never finds.
const (
	keyWorkspace = "workspace"
	keyTab       = "tab"
	keyPane      = "pane"
	keyItems     = "items"
	keyClosed    = "closed"
	keyLayout    = "layout"
)

// Workspace is API Spec §4's `Workspace`.
type Workspace struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	CWD         string   `json:"cwd"`
	OrderIndex  int      `json:"order_index"`
	RollupState string   `json:"rollup_state"`
	TabIDs      []string `json:"tab_ids"`
	CreatedAt   int64    `json:"created_at"`
	ClosedAt    *int64   `json:"closed_at"`
}

// Tab is API Spec §4's `Tab`.
type Tab struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Label         string `json:"label"`
	OrderIndex    int    `json:"order_index"`
	FocusedPaneID string `json:"focused_pane_id"`
	CreatedAt     int64  `json:"created_at"`
	ClosedAt      *int64 `json:"closed_at"`
}

// Pane is API Spec §4's `Pane`.
//
// `attention_state` and `state_source` are on the wire from F0 although nothing reports
// them yet: the field being absent and the field saying `unknown` are different messages,
// and a client written against F0 should not have to change when F1 starts filling them
// (delta `2026-09-pane-attention-state`).
type Pane struct {
	ID             string            `json:"id"`
	TabID          string            `json:"tab_id"`
	WorkspaceID    string            `json:"workspace_id"`
	SessionID      *string           `json:"session_id"`
	ThreadID       *string           `json:"thread_id"`
	Label          string            `json:"label"`
	CWD            string            `json:"cwd"`
	Command        []string          `json:"command,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Aliases        []string          `json:"aliases"`
	AttentionState string            `json:"attention_state"`
	StateSource    *string           `json:"state_source"`
	Metadata       map[string]any    `json:"metadata"`
	CreatedAt      int64             `json:"created_at"`
	ClosedAt       *int64            `json:"closed_at"`
}

func toWireWorkspace(w wsdomain.Workspace) Workspace {
	ids := w.TabIDs
	if ids == nil {
		ids = []string{}
	}
	return Workspace{
		ID: w.ID, Label: w.Label, CWD: w.CWD, OrderIndex: w.OrderIndex,
		RollupState: string(w.RollupState), TabIDs: ids,
		CreatedAt: w.CreatedAt.UnixMilli(), ClosedAt: millisPtr(w.ClosedAt),
	}
}

func toWireTab(t wsdomain.Tab) Tab {
	return Tab{
		ID: t.ID, WorkspaceID: t.WorkspaceID, Label: t.Label, OrderIndex: t.OrderIndex,
		FocusedPaneID: t.FocusedPaneID,
		CreatedAt:     t.CreatedAt.UnixMilli(), ClosedAt: millisPtr(t.ClosedAt),
	}
}

// Layout and LayoutNode are API Spec §4's `Layout` and its nodes.
//
// They exist here although `wsdomain` already has a JSON codec for the same tree, and the
// duplication is deliberate. Data Model §2.4b puts the portable tree in `tabs.layout_json`,
// which makes the domain codec a *storage* format the spec asks for; putting the same
// struct on the wire would make `internal/api` stop being the only package that knows the
// wire format (Tech Design line 340). It also fixes something visible: the stored form
// omits empty fields to keep the column small, and a client reading §4 expects
// `focused_pane_id` and `root` to be present — as null — on a tab whose last pane closed.
type Layout struct {
	WorkspaceID   string      `json:"workspace_id"`
	TabID         string      `json:"tab_id"`
	FocusedPaneID string      `json:"focused_pane_id"`
	Root          *LayoutNode `json:"root"`
}

// LayoutNode is one position in the tree: a pane or a split of two children.
type LayoutNode struct {
	Type string `json:"type"`

	PaneID  string            `json:"pane_id,omitempty"`
	Label   string            `json:"label,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	Direction string      `json:"direction,omitempty"`
	Ratio     float64     `json:"ratio,omitempty"`
	First     *LayoutNode `json:"first,omitempty"`
	Second    *LayoutNode `json:"second,omitempty"`
}

func toWireLayout(l wsdomain.Layout) Layout {
	return Layout{
		WorkspaceID: l.WorkspaceID, TabID: l.TabID,
		FocusedPaneID: l.FocusedPaneID, Root: toWireNode(l.Root),
	}
}

func toWireNode(n *wsdomain.Node) *LayoutNode {
	if n == nil {
		return nil
	}
	return &LayoutNode{
		Type: string(n.Type), PaneID: n.PaneID, Label: n.Label, CWD: n.CWD,
		Command: n.Command, Env: n.Env,
		Direction: string(n.Direction), Ratio: n.Ratio,
		First: toWireNode(n.First), Second: toWireNode(n.Second),
	}
}

func toWirePane(p wsdomain.Pane) Pane {
	aliases := p.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	return Pane{
		ID: p.ID, TabID: p.TabID, WorkspaceID: p.WorkspaceID,
		SessionID: emptyAsNull(p.SessionID), ThreadID: emptyAsNull(p.ThreadID),
		Label: p.Label, CWD: p.CWD, Command: p.Command, Env: p.Env,
		Aliases: aliases, AttentionState: string(p.AttentionState),
		StateSource: emptyAsNull(p.StateSource), Metadata: map[string]any{},
		CreatedAt: p.CreatedAt.UnixMilli(), ClosedAt: millisPtr(p.ClosedAt),
	}
}

// millisPtr renders a nullable timestamp as UTC epoch milliseconds (Art. 6), keeping the
// key present so a client can tell "still open" from "closed at an unknown time".
func millisPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}

// emptyAsNull renders an absent string as JSON null rather than "". API Spec §4 types these
// fields as `string | null`, and a client told a pane's session is the empty string would
// have to know that means "none".
func emptyAsNull(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// tree returns the workspaces port or the error a daemon without one owes its client.
func (c *conn) tree() (wsports.Workspaces, error) {
	if c.server.cfg.Workspaces == nil {
		return nil, fmt.Errorf("%w: the workspace tree is not wired in", ErrMethodNotFound)
	}
	return c.server.cfg.Workspaces, nil
}

// decode unmarshals parameters, turning a malformed object into the validation error the
// §5.4 table expects rather than a decoder error nobody can act on.
func decode(raw json.RawMessage, into any, method string) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return ValidationError(method + " parameters are not an object")
	}
	return nil
}

// --- workspace.* -----------------------------------------------------------------

type createWorkspaceParams struct {
	CWD      string `json:"cwd"`
	Label    string `json:"label"`
	TabLabel string `json:"tab_label"`
	Focus    *bool  `json:"focus"`
}

func handleWorkspaceCreate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params createWorkspaceParams
	if err := decode(raw, &params, "workspace.create"); err != nil {
		return nil, err
	}

	result, err := tree.CreateWorkspace(ctx, wsdomain.CreateWorkspaceParams{
		CWD: params.CWD, Label: params.Label, TabLabel: params.TabLabel,
		// `focus?: true` in §5.4 means the default is true, which a bare bool cannot say.
		Focus: optionalBool(params.Focus, true),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		keyWorkspace: toWireWorkspace(result.Workspace),
		keyTab:       toWireTab(result.Tab),
		"root_pane":  toWirePane(result.RootPane),
	}, nil
}

// optionalBool applies the default an omitted `focus?: true` implies.
func optionalBool(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func handleWorkspaceList(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	workspaces, err := tree.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Workspace, 0, len(workspaces))
	for _, w := range workspaces {
		items = append(items, toWireWorkspace(w))
	}
	return map[string]any{keyItems: items}, nil
}

type workspaceIDParams struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	ClosePanes  *bool  `json:"close_panes"`
}

func handleWorkspaceFocus(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params workspaceIDParams
	if err := decode(raw, &params, "workspace.focus"); err != nil {
		return nil, err
	}
	workspace, err := tree.FocusWorkspace(ctx, params.WorkspaceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyWorkspace: toWireWorkspace(workspace)}, nil
}

func handleWorkspaceRename(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params workspaceIDParams
	if err := decode(raw, &params, "workspace.rename"); err != nil {
		return nil, err
	}
	workspace, err := tree.RenameWorkspace(ctx, params.WorkspaceID, params.Label)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyWorkspace: toWireWorkspace(workspace)}, nil
}

func handleWorkspaceClose(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params workspaceIDParams
	if err := decode(raw, &params, "workspace.close"); err != nil {
		return nil, err
	}
	// §5.4: `close_panes?: true`. Leaving the terminals running is the deliberate choice,
	// not the default.
	if err := tree.CloseWorkspace(ctx, params.WorkspaceID, optionalBool(params.ClosePanes, true)); err != nil {
		return nil, err
	}
	return map[string]any{keyClosed: true}, nil
}

// --- tab.* -------------------------------------------------------------------------

type tabParams struct {
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Label       string `json:"label"`
	Focus       *bool  `json:"focus"`
}

func handleTabCreate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params tabParams
	if err := decode(raw, &params, "tab.create"); err != nil {
		return nil, err
	}
	tab, pane, err := tree.CreateTab(ctx, params.WorkspaceID, params.Label,
		optionalBool(params.Focus, true))
	if err != nil {
		return nil, err
	}
	return map[string]any{keyTab: toWireTab(tab), "root_pane": toWirePane(pane)}, nil
}

func handleTabList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params tabParams
	if err := decode(raw, &params, "tab.list"); err != nil {
		return nil, err
	}
	tabs, err := tree.ListTabs(ctx, params.WorkspaceID)
	if err != nil {
		return nil, err
	}
	items := make([]Tab, 0, len(tabs))
	for _, t := range tabs {
		items = append(items, toWireTab(t))
	}
	return map[string]any{keyItems: items}, nil
}

func handleTabFocus(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params tabParams
	if err := decode(raw, &params, "tab.focus"); err != nil {
		return nil, err
	}
	tab, err := tree.FocusTab(ctx, params.TabID)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyTab: toWireTab(tab)}, nil
}

func handleTabRename(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params tabParams
	if err := decode(raw, &params, "tab.rename"); err != nil {
		return nil, err
	}
	tab, err := tree.RenameTab(ctx, params.TabID, params.Label)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyTab: toWireTab(tab)}, nil
}

func handleTabClose(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params tabParams
	if err := decode(raw, &params, "tab.close"); err != nil {
		return nil, err
	}
	if err := tree.CloseTab(ctx, params.TabID); err != nil {
		return nil, err
	}
	return map[string]any{keyClosed: true}, nil
}

// --- pane.* ------------------------------------------------------------------------

type splitParams struct {
	PaneID    string            `json:"pane_id"`
	Direction string            `json:"direction"`
	Ratio     float64           `json:"ratio"`
	CWD       string            `json:"cwd"`
	Command   []string          `json:"command"`
	Env       map[string]string `json:"env"`
	Focus     *bool             `json:"focus"`
}

func handlePaneSplit(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params splitParams
	if err := decode(raw, &params, "pane.split"); err != nil {
		return nil, err
	}
	pane, layout, err := tree.SplitPane(ctx, wsdomain.SplitParams{
		PaneID: params.PaneID, Direction: wsdomain.SplitDirection(params.Direction),
		Ratio: params.Ratio, CWD: params.CWD, Command: params.Command, Env: params.Env,
		Focus: optionalBool(params.Focus, true),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{keyPane: toWirePane(pane), keyLayout: toWireLayout(layout)}, nil
}

type paneParams struct {
	PaneID string `json:"pane_id"`
	TabID  string `json:"tab_id"`
	Label  string `json:"label"`
}

func handlePaneList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params paneParams
	if err := decode(raw, &params, "pane.list"); err != nil {
		return nil, err
	}
	panes, err := tree.ListPanes(ctx, params.TabID)
	if err != nil {
		return nil, err
	}
	items := make([]Pane, 0, len(panes))
	for _, p := range panes {
		items = append(items, toWirePane(p))
	}
	return map[string]any{keyItems: items}, nil
}

func handlePaneGet(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params paneParams
	if err := decode(raw, &params, "pane.get"); err != nil {
		return nil, err
	}
	pane, err := tree.GetPane(ctx, params.PaneID)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyPane: toWirePane(pane)}, nil
}

func handlePaneFocus(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params paneParams
	if err := decode(raw, &params, "pane.focus"); err != nil {
		return nil, err
	}
	pane, err := tree.FocusPane(ctx, params.PaneID)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyPane: toWirePane(pane)}, nil
}

func handlePaneRename(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params paneParams
	if err := decode(raw, &params, "pane.rename"); err != nil {
		return nil, err
	}
	pane, err := tree.RenamePane(ctx, params.PaneID, params.Label)
	if err != nil {
		return nil, err
	}
	return map[string]any{keyPane: toWirePane(pane)}, nil
}

type moveParams struct {
	PaneID      string `json:"pane_id"`
	Destination struct {
		Type        string `json:"type"`
		TabID       string `json:"tab_id"`
		WorkspaceID string `json:"workspace_id"`
		Label       string `json:"label"`
	} `json:"destination"`
}

func handlePaneMove(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params moveParams
	if err := decode(raw, &params, "pane.move"); err != nil {
		return nil, err
	}
	moved, err := tree.MovePane(ctx, wsdomain.MoveParams{
		PaneID: params.PaneID,
		Destination: wsdomain.MoveDestination{
			Type:        wsdomain.MoveDestinationType(params.Destination.Type),
			TabID:       params.Destination.TabID,
			WorkspaceID: params.Destination.WorkspaceID,
			Label:       params.Destination.Label,
		},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		keyPane:                 toWirePane(moved.Pane),
		"previous_pane_id":      moved.PreviousPaneID,
		"previous_workspace_id": moved.PreviousWorkspaceID,
		keyLayout:               toWireLayout(moved.Layout),
	}, nil
}

func handlePaneClose(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var params paneParams
	if err := decode(raw, &params, "pane.close"); err != nil {
		return nil, err
	}
	if err := tree.ClosePane(ctx, params.PaneID); err != nil {
		return nil, err
	}
	return map[string]any{keyClosed: true}, nil
}
