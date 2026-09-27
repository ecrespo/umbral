// Package ports declares what the workspace tree publishes to the rest of the daemon and
// what it needs from the outside world.
//
// The api layer holds the inbound interface and never the concrete service, and the service
// reaches SQLite and the terminal module only through the two outbound ones. That is what
// keeps Tech Design §5.2's rule — `workspaces` depends on the *ports* of `sessions` and
// `store`, never on their adapters — enforceable rather than merely stated.
package ports

import (
	"context"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// Workspaces is the module's inbound port: the `workspace.*`, `tab.*` and `pane.*` surface
// of API Spec §5.4 to §5.6.
//
// `Restore` is deliberately absent. It is called once, by the composition root, on the
// concrete service — `api` has no business rebuilding the tree, and putting it here would
// both widen what the wire layer can reach and make every fake in every wire test implement
// a method none of them exercises.
type Workspaces interface {
	// CreateWorkspace builds a workspace, its first tab and its root pane and returns all
	// three (REQ-WS-001). One call, one response, never a half-built tree.
	CreateWorkspace(ctx context.Context, params domain.CreateWorkspaceParams) (domain.Tree, error)
	// ListWorkspaces reports the open workspaces with their rollup state (REQ-WS-006).
	ListWorkspaces(ctx context.Context) ([]domain.Workspace, error)
	// FocusWorkspace marks a workspace as the focused one.
	FocusWorkspace(ctx context.Context, id string) (domain.Workspace, error)
	// RenameWorkspace changes a workspace's label.
	RenameWorkspace(ctx context.Context, id, label string) (domain.Workspace, error)
	// CloseWorkspace closes a workspace and, when asked, the terminals of its panes.
	CloseWorkspace(ctx context.Context, id string, closePanes bool) error

	// CreateTab adds a tab with its root pane (API Spec §5.5).
	CreateTab(ctx context.Context, workspaceID, label string, focus bool) (domain.Tab, domain.Pane, error)
	// ListTabs reports a workspace's open tabs in order.
	ListTabs(ctx context.Context, workspaceID string) ([]domain.Tab, error)
	// FocusTab marks a tab as its workspace's focused one.
	FocusTab(ctx context.Context, id string) (domain.Tab, error)
	// RenameTab changes a tab's label.
	RenameTab(ctx context.Context, id, label string) (domain.Tab, error)
	// CloseTab closes a tab and the panes under it.
	CloseTab(ctx context.Context, id string) error

	// SplitPane creates a pane beside an existing one, attaches a terminal to it, and
	// returns the pane with the tab's new layout (REQ-WS-003).
	SplitPane(ctx context.Context, params domain.SplitParams) (domain.Pane, domain.Layout, error)
	// ListPanes reports a tab's panes in layout order.
	ListPanes(ctx context.Context, tabID string) ([]domain.Pane, error)
	// GetPane reports one pane. A previous identifier resolves through its alias
	// (REQ-WS-007).
	GetPane(ctx context.Context, id string) (domain.Pane, error)
	// FocusPane marks a pane as its tab's focused one.
	FocusPane(ctx context.Context, id string) (domain.Pane, error)
	// RenamePane changes a pane's label.
	RenamePane(ctx context.Context, id, label string) (domain.Pane, error)
	// MovePane moves a pane, keeping its terminal and its process. The pane gets a new
	// identifier and the old one stays resolvable (REQ-WS-007); the previous identifier
	// and workspace are returned so the caller can emit `pane.moved`.
	MovePane(ctx context.Context, params domain.MoveParams) (Moved, error)
	// ClosePane closes one pane and terminates its session.
	ClosePane(ctx context.Context, id string) error

	// ExportLayout returns a tab's tree (REQ-WS-004). An empty tabID means the focused
	// tab, which is API Spec §5.7's default.
	ExportLayout(ctx context.Context, tabID string) (domain.Layout, error)
	// ApplyLayout creates a tab reproducing a portable tree, and says in its result what
	// it did not reproduce (REQ-WS-005).
	ApplyLayout(ctx context.Context, params domain.ApplyLayoutParams) (domain.Applied, error)

	// Focused reports the workspace and tab a client should open on, which
	// `layout.export` defaults to and `session.snapshot` reports (API Spec §5.3).
	Focused() (workspaceID, tabID string)

	// Snapshot returns every open workspace, tab, pane and layout in one read
	// (REQ-API-001). It is assembled here rather than in `api` because it is several
	// queries whose order matters, and that is the module's business, not the wire's.
	Snapshot(ctx context.Context) (domain.Snapshot, error)
}

// Restored is what a restart finds waiting in the database (REQ-TERM-009).
//
// The panes come back with their session cleared, because the terminals they named died with
// the previous daemon, and with `CommandPending` set wherever a command was stored: a restart
// never runs one (REQ-TERM-011).
type Restored struct {
	Panes []domain.Pane
	Focus domain.Focus
}

// Moved is what a completed `pane.move` reports. The previous identifiers are in the
// result rather than left for the caller to remember, because API Spec §6 puts them in the
// `pane.moved` notification and a caller that had to cache them would be one refactor away
// from sending the wrong pair.
type Moved struct {
	Pane                domain.Pane
	PreviousPaneID      string
	PreviousWorkspaceID string
	Layout              domain.Layout
	// SourceLayout is the tree the pane left, which changed too: a client showing the old
	// tab has to redraw it.
	SourceLayout domain.Layout
	// CreatedWorkspace and CreatedTab are what a `new_workspace` or `new_tab` destination
	// had to build. They are reported because §6 defines `workspace.created` and
	// `tab.created`, and a client that learned of a new tab only from the `layout` inside
	// `pane.moved` would have to infer an object's existence from a reference to it.
	CreatedWorkspace *domain.Workspace
	CreatedTab       *domain.Tab
}

// Terminals is the narrow view of the sessions module a pane needs: somewhere to get a
// terminal and somewhere to give it back.
//
// It is declared here rather than taken as `sessions/ports.Sessions` so that the tree
// depends on the two operations it actually performs. A pane never reads a screen, never
// writes input and never resizes anything — those belong to whoever is displaying it — and
// a wide interface would have invited it to.
type Terminals interface {
	Create(ctx context.Context, params sessdomain.CreateParams) (sessdomain.Session, error)
	Close(ctx context.Context, id string) error
}

// Screens is the narrow view of the sessions module that REQ-TERM-010's capture needs: the
// current screen of one session, as a VT stream that replays into an empty emulator.
//
// It is separate from Terminals rather than folded into it because the two are used under
// different conditions — Terminals on every pane, this one only while pane history is on —
// and because a port that can read screens is one a future change could quietly use for
// something the user did not opt into.
type Screens interface {
	Screen(ctx context.Context, sessionID string) (Screen, error)
}

// Screen is a pane's current display, in this module's own terms.
//
// It is not `sessions/ports.Snapshot`, and the boundary rules are right to refuse that:
// `workspaces` depends on the *domain* of `sessions` and never on its ports, so a type
// declared here and adapted by the composition root is what keeps the two modules from
// growing into one. The cost is a four-line struct; the benefit is that a change to what
// `session.subscribe` returns is not automatically a change to what a restart stores.
type Screen struct {
	// Data is a VT stream that replays into an empty emulator.
	Data []byte
	// Rows is how many lines it represents, which Data Model §2.4d stores beside it.
	Rows int
}

// Tree is the persistence port: the whole tree's storage, in one interface because every
// operation on it spans at least two tables and has to be one transaction.
//
// `workspace.create` writes a workspace, a tab and a pane; a split writes a pane and the
// tab's layout; a move writes two layouts, a pane and an alias. Splitting this into a
// repository per table would put the transaction boundary above the port, where the service
// would have to know SQL semantics to place it.
type Tree interface {
	// CreateWorkspace allocates `w<n>`, `w<n>:t<m>` and `w<n>:p<m>` and writes all three.
	CreateWorkspace(ctx context.Context, params domain.CreateWorkspaceParams) (domain.Tree, error)
	// ListWorkspaces reports the open workspaces, each with its tab identifiers.
	ListWorkspaces(ctx context.Context) ([]domain.Workspace, error)
	// GetWorkspace reports one workspace, or domain.ErrNotFound.
	GetWorkspace(ctx context.Context, id string) (domain.Workspace, error)
	// SetWorkspaceLabel renames a workspace.
	SetWorkspaceLabel(ctx context.Context, id, label string) (domain.Workspace, error)
	// CloseWorkspace marks a workspace and everything under it closed, and reports the
	// sessions that were attached so the caller can terminate them.
	CloseWorkspace(ctx context.Context, id string) ([]string, error)

	// Restore reads what a restart has to rebuild and clears the previous run's sessions
	// in the same transaction (REQ-TERM-009, Data Model §6 step 5).
	Restore(ctx context.Context) (Restored, error)
	// SetFocus persists which workspace is focused and which tab inside it, so the answer
	// survives a restart instead of living only in the service.
	SetFocus(ctx context.Context, workspaceID, tabID string, atMillis int64) error
	// SetCommandPending records that a pane's stored command has not been run
	// (REQ-TERM-011).
	//
	// There is no clearing counterpart, and that is the design rather than an omission: no
	// spec sentence says when a command stops being pending, because the daemon never
	// learns. It types the command at the prompt and stops watching — whether the user
	// presses Enter, edits it or abandons it with Ctrl-C happens inside the PTY, and a
	// daemon that guessed would report a command as run that never was. The flag says
	// "Umbral did not run this", which stays true for the pane's whole life.
	SetCommandPending(ctx context.Context, paneID string) error

	// SaveScreen, LoadScreen and ForgetScreens are REQ-TERM-010's opt-in replay. They are
	// called only while the setting is on, except ForgetScreens, which runs once at start
	// when it is off — Data Model §2.4d makes turning it off a promise that what was
	// captured stops existing.
	SaveScreen(ctx context.Context, paneID string, screen []byte, rows int, atMillis int64) error
	LoadScreen(ctx context.Context, paneID string) ([]byte, error)
	ForgetScreens(ctx context.Context) error

	// CreateTab allocates a tab and its root pane inside an existing workspace.
	CreateTab(ctx context.Context, workspaceID, label string) (domain.Tab, domain.Pane, error)
	// ListTabs reports a workspace's open tabs.
	ListTabs(ctx context.Context, workspaceID string) ([]domain.Tab, error)
	// GetTab reports one tab.
	GetTab(ctx context.Context, id string) (domain.Tab, error)
	// SetTabLabel renames a tab.
	SetTabLabel(ctx context.Context, id, label string) (domain.Tab, error)
	// SetFocusedPane records which pane a tab is focused on.
	SetFocusedPane(ctx context.Context, tabID, paneID string) error
	// CloseTab marks a tab and its panes closed and reports their sessions.
	CloseTab(ctx context.Context, id string) ([]string, error)

	// CreatePane allocates a pane in a tab and rewrites the tab's layout in the same
	// transaction, so a pane can never exist outside the tree that positions it.
	CreatePane(ctx context.Context, tabID string, params domain.SplitParams, layout *domain.Node) (domain.Pane, error)
	// AttachSession binds a live terminal to a pane.
	AttachSession(ctx context.Context, paneID, sessionID string) error
	// ListPanes reports a tab's open panes.
	ListPanes(ctx context.Context, tabID string) ([]domain.Pane, error)
	// GetPane reports one pane, resolving a previous identifier through its alias.
	GetPane(ctx context.Context, id string) (domain.Pane, error)
	// SetPaneLabel renames a pane and updates the label the layout carries.
	SetPaneLabel(ctx context.Context, id, label string) (domain.Pane, error)
	// MovePane renames a pane into its destination, records the old identifier as an
	// alias and rewrites both tabs' layouts, all in one transaction.
	MovePane(ctx context.Context, params domain.MoveParams) (Moved, error)
	// ClosePane marks one pane closed, removes it from its tab's layout and reports its
	// session.
	ClosePane(ctx context.Context, id string) (sessionID string, err error)

	// Layout reports a tab's stored tree.
	Layout(ctx context.Context, tabID string) (domain.Layout, error)
	// ApplyLayout writes a whole tab from a portable tree, in one transaction.
	ApplyLayout(ctx context.Context, params domain.ApplyLayoutParams) (domain.Applied, error)
}
