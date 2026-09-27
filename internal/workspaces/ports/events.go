package ports

import (
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// Event kinds published by the workspace tree. The names match the notification methods of
// API Spec §6, so an event can be followed from the bus to the wire without a translation
// table — the same convention `sessions/ports` uses.
const (
	KindWorkspaceCreated bus.Kind = "workspace.created"
	KindWorkspaceUpdated bus.Kind = "workspace.updated"
	KindWorkspaceClosed  bus.Kind = "workspace.closed"
	KindWorkspaceFocused bus.Kind = "workspace.focused"

	KindTabCreated bus.Kind = "tab.created"
	KindTabClosed  bus.Kind = "tab.closed"
	KindTabFocused bus.Kind = "tab.focused"

	KindPaneCreated bus.Kind = "pane.created"
	KindPaneUpdated bus.Kind = "pane.updated"
	KindPaneClosed  bus.Kind = "pane.closed"
	KindPaneFocused bus.Kind = "pane.focused"
	KindPaneMoved   bus.Kind = "pane.moved"

	KindLayoutUpdated bus.Kind = "layout.updated"
)

// These live in ports rather than domain because they carry a bus.Kind, and domain may
// import nothing but the standard library (Art. 3).

// WorkspaceEvent carries a workspace for the four `workspace.*` notifications. One type
// serves all four because §6 gives them one payload and the kind is what distinguishes
// them; a type per verb would be four identical structs.
type WorkspaceEvent struct {
	Kind      bus.Kind
	Workspace domain.Workspace
}

// EventKind implements bus.Event.
func (e WorkspaceEvent) EventKind() bus.Kind { return e.Kind }

// TabEvent carries a tab for the three `tab.*` notifications.
type TabEvent struct {
	Kind bus.Kind
	Tab  domain.Tab
}

// EventKind implements bus.Event.
func (e TabEvent) EventKind() bus.Kind { return e.Kind }

// PaneEvent carries a pane for the four `pane.*` notifications that take one.
type PaneEvent struct {
	Kind bus.Kind
	Pane domain.Pane
}

// EventKind implements bus.Event.
func (e PaneEvent) EventKind() bus.Kind { return e.Kind }

// PaneMoved is REQ-WS-007's notification, and the reason it has a type of its own: §6 gives
// it a payload none of the others has — `{pane, previous_pane_id, previous_workspace_id,
// layout}` — and the requirement is explicit that this is emitted *instead of* a close and
// create pair, so a client can follow the terminal across the move rather than watching one
// pane die and an unrelated one appear.
type PaneMoved struct {
	Pane                domain.Pane
	PreviousPaneID      string
	PreviousWorkspaceID string
	Layout              domain.Layout
}

// EventKind implements bus.Event.
func (PaneMoved) EventKind() bus.Kind { return KindPaneMoved }

// LayoutUpdated is §6's `layout.updated`, whose payload is a `Layout`. It is published
// whenever a tab's tree changes shape — a split, a close, a move in or out, an apply — so a
// client that draws from the tree does not have to reconstruct it from the pane events.
type LayoutUpdated struct {
	Layout domain.Layout
}

// EventKind implements bus.Event.
func (LayoutUpdated) EventKind() bus.Kind { return KindLayoutUpdated }
