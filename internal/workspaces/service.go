// Package workspaces owns the workspace tree: workspaces, tabs, panes, their public
// identifiers, their layout and the rollup a sidebar draws (Tech Design §5.2, REQ-WS-001 to
// REQ-WS-007).
//
// It is the orchestrator. The tree's storage is behind ports.Tree, the terminals behind
// ports.Terminals, and neither knows about the other: what this file does is decide the
// order in which they are touched and what is announced afterwards.
package workspaces

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
	"github.com/ecrespo/umbral/internal/workspaces/ports"
)

// defaultPaneSize is what a pane's terminal is created with.
//
// Nothing in the API says: `pane.split` takes a direction and a ratio and no size, because
// the size depends on the window the client is drawing in and the daemon cannot see it. So
// the shell starts at the classic 80x24 and the client's first `session.resize` corrects
// it — the same order a terminal emulator uses, and the reason REQ-TERM-007 exists.
var defaultPaneSize = sessdomain.Size{Cols: 80, Rows: 24}

// Config wires the service.
type Config struct {
	// Tree is the tree's persistence.
	Tree ports.Tree
	// Terminals creates and closes the sessions panes host. A nil value leaves panes
	// without terminals, which is what the tests of the tree's own shape use.
	Terminals ports.Terminals
	// Bus carries the §6 notifications. A nil value publishes nothing.
	Bus *bus.Bus
	// Shell is the program a new pane runs. Empty means the session module's default.
	Shell string
	// Logger reports what a restart rebuilt and what it could not. A nil value discards.
	Logger *slog.Logger
	// PaneHistory turns on REQ-TERM-010's capture and replay. Off unless
	// `$XDG_CONFIG_HOME/umbral/config.toml` says otherwise, because pane output can
	// contain secrets and the requirement makes that the default.
	PaneHistory bool
	// Screens reads a pane's current screen for that capture. Nil disables capture
	// whatever the setting says.
	Screens ports.Screens
}

// Service is the module's inbound port in the flesh.
type Service struct {
	tree      ports.Tree
	terminals ports.Terminals
	bus       *bus.Bus
	shell     string
	log       *slog.Logger

	paneHistory bool
	screens     ports.Screens

	// focus is which workspace and tab a client should open on, cached here because every
	// `layout.export` with no `tab_id` and every `session.snapshot` asks for it and
	// neither should cost a query. The durable copy lives in
	// `workspaces.focused_tab_id`/`focused_at` (migration 0004), which is what a restart
	// reads: before T-F0-18 this was the only copy and the answer was lost on restart.
	// API Spec §5.7 defaults `layout.export` to it and §5.3 reports it.
	mu           sync.RWMutex
	focusedWS    string
	focusedTabOf map[string]string
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	if cfg.Tree == nil {
		return nil, fmt.Errorf("workspaces: a Tree is required")
	}
	return &Service{
		tree: cfg.Tree, terminals: cfg.Terminals, bus: cfg.Bus, shell: cfg.Shell,
		log:          logger(cfg.Logger),
		paneHistory:  cfg.PaneHistory,
		screens:      cfg.Screens,
		focusedTabOf: map[string]string{},
	}, nil
}

var _ ports.Workspaces = (*Service)(nil)

func (s *Service) publish(ev bus.Event) {
	if s.bus != nil {
		s.bus.Publish(ev)
	}
}

// checkLabel keeps a label inside the cap. The API sets no number; domain does, and the
// reason is in its comment.
func checkLabel(label string) error {
	if len(label) > domain.MaxLabelLength {
		return fmt.Errorf("%w: a label is %d characters, the cap is %d",
			domain.ErrValidation, len(label), domain.MaxLabelLength)
	}
	return nil
}

// The identifier checks at the edge. Without them a malformed identifier reaches the store,
// finds nothing and comes back as NOT_FOUND — which tells a client its pane is gone when in
// fact its request was malformed. API Spec §3 maps an invalid parameter to
// VALIDATION_ERROR, and `w1:t1` handed to `pane.focus` is an invalid parameter, not a
// missing pane.
func checkWorkspaceID(id string) error {
	if _, ok := domain.ParseWorkspaceID(id); !ok {
		return fmt.Errorf("%w: %q is not a workspace identifier of the form w<n>",
			domain.ErrValidation, id)
	}
	return nil
}

func checkTabID(id string) error {
	if _, _, ok := domain.ParseTabID(id); !ok {
		return fmt.Errorf("%w: %q is not a tab identifier of the form w<n>:t<m>",
			domain.ErrValidation, id)
	}
	return nil
}

func checkPaneID(id string) error {
	if _, _, ok := domain.ParsePaneID(id); !ok {
		return fmt.Errorf("%w: %q is not a pane identifier of the form w<n>:p<m>",
			domain.ErrValidation, id)
	}
	return nil
}

// --- workspaces ---------------------------------------------------------------

// CreateWorkspace is REQ-WS-001: one call returns the workspace, its first tab and its root
// pane, with a terminal already attached to the pane.
//
// The tree is written first and the terminal attached second, which is the order that fails
// safely. A terminal created before the tree would be orphaned by a failed insert — a live
// shell nothing points at — while a pane written before its terminal is just a pane without
// one yet, which is a state the schema models (`session_id` is nullable) and a client can
// see.
func (s *Service) CreateWorkspace(ctx context.Context, params domain.CreateWorkspaceParams) (domain.Tree, error) {
	if err := checkLabel(params.Label); err != nil {
		return domain.Tree{}, err
	}
	if err := checkLabel(params.TabLabel); err != nil {
		return domain.Tree{}, err
	}
	// §5.4 makes `cwd` the one required parameter. Without it the root pane's shell starts
	// wherever the daemon happens to be, which is never what the caller meant and stays
	// invisible until they run `pwd`.
	if params.CWD == "" {
		return domain.Tree{}, fmt.Errorf("%w: workspace.create requires a cwd", domain.ErrValidation)
	}

	tree, err := s.tree.CreateWorkspace(ctx, params)
	if err != nil {
		return domain.Tree{}, err
	}

	pane, err := s.attach(ctx, tree.RootPane)
	if err != nil {
		return domain.Tree{}, err
	}
	tree.RootPane = pane

	// Through the rule rather than the constant the store filled in. They agree today
	// only because F0 has no producer of a pane state; the day one exists, a create that
	// answered `unknown` while `list` answered something else would be a bug nobody could
	// explain.
	if tree.Workspace.RollupState, err = s.rollup(ctx, tree.Workspace); err != nil {
		return domain.Tree{}, err
	}

	s.publish(ports.WorkspaceEvent{Kind: ports.KindWorkspaceCreated, Workspace: tree.Workspace})
	s.publish(ports.TabEvent{Kind: ports.KindTabCreated, Tab: tree.Tab})
	s.publish(ports.PaneEvent{Kind: ports.KindPaneCreated, Pane: tree.RootPane})
	if params.Focus {
		s.setFocusedWorkspace(ctx, tree.Workspace.ID)
		s.setFocusedTab(ctx, tree.Workspace.ID, tree.Tab.ID)
		s.publish(ports.WorkspaceEvent{Kind: ports.KindWorkspaceFocused, Workspace: tree.Workspace})
	}
	return tree, nil
}

// attachPending gives an applied pane a shell and leaves its command waiting (REQ-TERM-011).
//
// It is `attach`'s sibling rather than a flag on it, because the two differ in what they
// promise: `attach` runs what the pane says it runs, which is what `pane.split` asks for;
// this one deliberately does not, and a boolean argument at the call site would have made
// that the less visible of the two.
func (s *Service) attachPending(ctx context.Context, pane domain.Pane) (domain.Pane, error) {
	if len(pane.Command) == 0 {
		return s.attach(ctx, pane)
	}
	if s.terminals == nil {
		pane.CommandPending = true
		return pane, nil
	}

	session, err := s.terminals.Create(ctx, sessdomain.CreateParams{
		Shell: s.shell, CWD: pane.CWD, Env: pane.Env, Size: defaultPaneSize,
		ShellIntegration: true,
		TypeAtPrompt:     []byte(shellLine(pane.Command)),
	})
	if err != nil {
		return domain.Pane{}, fmt.Errorf("workspaces: start the pane's terminal: %w", err)
	}
	if err := s.tree.AttachSession(ctx, pane.ID, session.ID); err != nil {
		_ = s.terminals.Close(ctx, session.ID)
		return domain.Pane{}, err
	}
	if err := s.tree.SetCommandPending(ctx, pane.ID); err != nil {
		return domain.Pane{}, err
	}
	pane.SessionID = session.ID
	pane.CommandPending = true
	return pane, nil
}

// attach gives a pane a terminal. A pane whose terminal cannot start is reported as an
// error rather than returned terminal-less, because a client that asked for a shell and got
// a pane would have no way to tell the difference until it typed into it.
func (s *Service) attach(ctx context.Context, pane domain.Pane) (domain.Pane, error) {
	if s.terminals == nil {
		return pane, nil
	}
	session, err := s.terminals.Create(ctx, sessdomain.CreateParams{
		Shell: s.shell, CWD: pane.CWD, Env: pane.Env, Size: defaultPaneSize,
		Command: pane.Command,
		// A pane running a command gets no integration, because there is nothing to
		// inject a bootstrap into; the sessions module says so too, and asking for it here
		// would be asking for something the answer to is always no.
		ShellIntegration: len(pane.Command) == 0,
	})
	if err != nil {
		return domain.Pane{}, fmt.Errorf("workspaces: start the pane's terminal: %w", err)
	}
	if err := s.tree.AttachSession(ctx, pane.ID, session.ID); err != nil {
		// The terminal is already running, and nothing points at it. Closing it here is
		// what keeps a failed attach from leaking a shell.
		_ = s.terminals.Close(ctx, session.ID)
		return domain.Pane{}, err
	}
	pane.SessionID = session.ID
	return pane, nil
}

// ListWorkspaces reports the open workspaces, each with the rollup REQ-WS-006 defines.
func (s *Service) ListWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	workspaces, err := s.tree.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	for i := range workspaces {
		state, err := s.rollup(ctx, workspaces[i])
		if err != nil {
			return nil, err
		}
		workspaces[i].RollupState = state
	}
	return workspaces, nil
}

// rollup gathers a workspace's children and applies REQ-WS-006's rule.
//
// In F0 every pane is `unknown`, so every workspace holding one rolls up to `unknown` and
// an empty one to `idle` — delta `2026-09-pane-attention-state` records why that is the
// honest answer rather than reporting `idle` for both. Threads are the other child the
// requirement names; they arrive in F1 and will be gathered here beside the panes.
func (s *Service) rollup(ctx context.Context, ws domain.Workspace) (domain.AttentionState, error) {
	var states []domain.AttentionState
	for _, tabID := range ws.TabIDs {
		panes, err := s.tree.ListPanes(ctx, tabID)
		if err != nil {
			return "", err
		}
		for _, pane := range panes {
			states = append(states, pane.AttentionState)
		}
	}
	return domain.Rollup(states), nil
}

// FocusWorkspace marks a workspace focused.
func (s *Service) FocusWorkspace(ctx context.Context, id string) (domain.Workspace, error) {
	if err := checkWorkspaceID(id); err != nil {
		return domain.Workspace{}, err
	}
	ws, err := s.tree.GetWorkspace(ctx, id)
	if err != nil {
		return domain.Workspace{}, err
	}
	if ws.RollupState, err = s.rollup(ctx, ws); err != nil {
		return domain.Workspace{}, err
	}
	s.setFocusedWorkspace(ctx, ws.ID)
	s.publish(ports.WorkspaceEvent{Kind: ports.KindWorkspaceFocused, Workspace: ws})
	return ws, nil
}

// RenameWorkspace changes a workspace's label.
func (s *Service) RenameWorkspace(ctx context.Context, id, label string) (domain.Workspace, error) {
	if err := checkWorkspaceID(id); err != nil {
		return domain.Workspace{}, err
	}
	if err := checkLabel(label); err != nil {
		return domain.Workspace{}, err
	}
	ws, err := s.tree.SetWorkspaceLabel(ctx, id, label)
	if err != nil {
		return domain.Workspace{}, err
	}
	if ws.RollupState, err = s.rollup(ctx, ws); err != nil {
		return domain.Workspace{}, err
	}
	s.publish(ports.WorkspaceEvent{Kind: ports.KindWorkspaceUpdated, Workspace: ws})
	return ws, nil
}

// CloseWorkspace closes a workspace and, when asked, the terminals under it.
//
// `close_panes` defaults to true in API Spec §5.4, and the false case is what keeps a
// workspace's shells running while its window goes away — the daemon owns them, not the
// client (DD-001, REQ-TERM-003).
func (s *Service) CloseWorkspace(ctx context.Context, id string, closePanes bool) error {
	if err := checkWorkspaceID(id); err != nil {
		return err
	}
	ws, err := s.tree.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	// The tabs and panes are read before the close, because afterwards there is nothing
	// open left to read — and §6 defines a notification for each of them. A client caching
	// the tree that heard only `workspace.closed` would keep every tab and pane under it
	// as a ghost.
	closing, err := s.descendants(ctx, ws)
	if err != nil {
		return err
	}

	sessions, err := s.tree.CloseWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if closePanes {
		s.closeSessions(ctx, sessions)
	}

	s.announceClosed(closing)
	s.forgetWorkspace(id)
	// The record that goes on the wire says closed. The one read above still said open,
	// because at that point it was.
	ws.ClosedAt = closedNow()
	s.publish(ports.WorkspaceEvent{Kind: ports.KindWorkspaceClosed, Workspace: ws})
	return nil
}

// subtree is what a close is about to remove, captured while it can still be read.
type subtree struct {
	tabs  []domain.Tab
	panes []domain.Pane
}

func (s *Service) descendants(ctx context.Context, ws domain.Workspace) (subtree, error) {
	var out subtree
	for _, tabID := range ws.TabIDs {
		tab, err := s.tree.GetTab(ctx, tabID)
		if err != nil {
			return subtree{}, err
		}
		panes, err := s.tree.ListPanes(ctx, tabID)
		if err != nil {
			return subtree{}, err
		}
		out.tabs = append(out.tabs, tab)
		out.panes = append(out.panes, panes...)
	}
	return out, nil
}

// announceClosed emits the §6 notification for every object a close removed, panes before
// tabs so a client unwinds its tree in the order it built it.
func (s *Service) announceClosed(t subtree) {
	now := closedNow()
	for _, pane := range t.panes {
		pane.ClosedAt = now
		s.publish(ports.PaneEvent{Kind: ports.KindPaneClosed, Pane: pane})
	}
	for _, tab := range t.tabs {
		tab.ClosedAt = now
		s.publish(ports.TabEvent{Kind: ports.KindTabClosed, Tab: tab})
	}
}

// closedNow stamps a closing record for the notification. The store writes its own
// `closed_at` on the row; this is the same moment said in the message about it.
func closedNow() *time.Time {
	t := time.Now().UTC()
	return &t
}

// closeSessions terminates terminals, reporting nothing.
//
// A session that will not close is not a reason to leave the tree half-closed: the rows are
// already committed, the caller has been told the workspace is gone, and the terminal
// module logs its own failures. Returning an error here would ask a client to retry a close
// that already happened.
func (s *Service) closeSessions(ctx context.Context, sessions []string) {
	if s.terminals == nil {
		return
	}
	for _, id := range sessions {
		_ = s.terminals.Close(ctx, id)
	}
}

// --- tabs -----------------------------------------------------------------------

// CreateTab adds a tab with its root pane (API Spec §5.5).
func (s *Service) CreateTab(ctx context.Context, workspaceID, label string, focus bool) (domain.Tab, domain.Pane, error) {
	if err := checkWorkspaceID(workspaceID); err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}
	if err := checkLabel(label); err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}
	tab, pane, err := s.tree.CreateTab(ctx, workspaceID, label)
	if err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}
	if pane, err = s.attach(ctx, pane); err != nil {
		return domain.Tab{}, domain.Pane{}, err
	}

	s.publish(ports.TabEvent{Kind: ports.KindTabCreated, Tab: tab})
	s.publish(ports.PaneEvent{Kind: ports.KindPaneCreated, Pane: pane})
	if focus {
		s.setFocusedTab(ctx, tab.WorkspaceID, tab.ID)
		s.publish(ports.TabEvent{Kind: ports.KindTabFocused, Tab: tab})
	}
	return tab, pane, nil
}

// ListTabs reports a workspace's open tabs.
func (s *Service) ListTabs(ctx context.Context, workspaceID string) ([]domain.Tab, error) {
	if err := checkWorkspaceID(workspaceID); err != nil {
		return nil, err
	}
	return s.tree.ListTabs(ctx, workspaceID)
}

// FocusTab marks a tab focused.
func (s *Service) FocusTab(ctx context.Context, id string) (domain.Tab, error) {
	if err := checkTabID(id); err != nil {
		return domain.Tab{}, err
	}
	tab, err := s.tree.GetTab(ctx, id)
	if err != nil {
		return domain.Tab{}, err
	}
	s.setFocusedTab(ctx, tab.WorkspaceID, tab.ID)
	s.publish(ports.TabEvent{Kind: ports.KindTabFocused, Tab: tab})
	return tab, nil
}

// RenameTab changes a tab's label.
func (s *Service) RenameTab(ctx context.Context, id, label string) (domain.Tab, error) {
	if err := checkTabID(id); err != nil {
		return domain.Tab{}, err
	}
	if err := checkLabel(label); err != nil {
		return domain.Tab{}, err
	}
	return s.tree.SetTabLabel(ctx, id, label)
}

// CloseTab closes a tab and terminates the terminals under it.
func (s *Service) CloseTab(ctx context.Context, id string) error {
	if err := checkTabID(id); err != nil {
		return err
	}
	tab, err := s.tree.GetTab(ctx, id)
	if err != nil {
		return err
	}
	panes, err := s.tree.ListPanes(ctx, id)
	if err != nil {
		return err
	}
	sessions, err := s.tree.CloseTab(ctx, id)
	if err != nil {
		return err
	}
	s.closeSessions(ctx, sessions)

	s.announceClosed(subtree{tabs: []domain.Tab{tab}, panes: panes})
	s.forgetTab(tab.WorkspaceID, tab.ID)
	return nil
}

// --- panes -----------------------------------------------------------------------

// SplitPane is REQ-WS-003: a new pane in the given position, with a terminal attached, and
// the tab's resulting layout.
func (s *Service) SplitPane(ctx context.Context, params domain.SplitParams) (domain.Pane, domain.Layout, error) {
	if !params.Direction.Valid() {
		return domain.Pane{}, domain.Layout{}, fmt.Errorf(
			"%w: direction %q is not \"right\" or \"down\"", domain.ErrValidation, params.Direction,
		)
	}
	if err := checkPaneID(params.PaneID); err != nil {
		return domain.Pane{}, domain.Layout{}, err
	}
	params.Ratio = domain.ClampRatio(params.Ratio)

	origin, err := s.tree.GetPane(ctx, params.PaneID)
	if err != nil {
		return domain.Pane{}, domain.Layout{}, err
	}
	// A split of a pane reached through an old alias splits the pane that alias resolves
	// to, so the layout operation below uses the pane's current identifier and not the one
	// the caller happened to hold.
	params.PaneID = origin.ID
	if params.CWD == "" {
		params.CWD = origin.CWD
	}

	layout, err := s.tree.Layout(ctx, origin.TabID)
	if err != nil {
		return domain.Pane{}, domain.Layout{}, err
	}
	grown, err := layout.Root.SplitAt(params.PaneID, params.Direction, params.Ratio,
		&domain.Node{Type: domain.NodePane, PaneID: placeholderPaneID})
	if err != nil {
		if errors.Is(err, domain.ErrPaneNotInLayout) {
			return domain.Pane{}, domain.Layout{}, fmt.Errorf("%w: pane %q", domain.ErrNotFound, params.PaneID)
		}
		return domain.Pane{}, domain.Layout{}, err
	}

	pane, err := s.tree.CreatePane(ctx, origin.TabID, params, grown)
	if err != nil {
		return domain.Pane{}, domain.Layout{}, err
	}
	if pane, err = s.attach(ctx, pane); err != nil {
		return domain.Pane{}, domain.Layout{}, err
	}

	updated, err := s.tree.Layout(ctx, origin.TabID)
	if err != nil {
		return domain.Pane{}, domain.Layout{}, err
	}

	s.publish(ports.PaneEvent{Kind: ports.KindPaneCreated, Pane: pane})
	s.publish(ports.LayoutUpdated{Layout: updated})
	if params.Focus {
		s.publish(ports.PaneEvent{Kind: ports.KindPaneFocused, Pane: pane})
	}
	return pane, updated, nil
}

// placeholderPaneID is the leaf the layout carries until the store allocates the real
// identifier, which it can only do inside the transaction that inserts the row. It is
// declared here and in treestore because the two sides of that handover have to agree, and
// it is `w0:p0` because `w0` is outside the REQ-WS-002 grammar: no real pane can collide
// with it.
const placeholderPaneID = "w0:p0"

// ListPanes reports a tab's panes in layout order.
func (s *Service) ListPanes(ctx context.Context, tabID string) ([]domain.Pane, error) {
	if err := checkTabID(tabID); err != nil {
		return nil, err
	}
	return s.tree.ListPanes(ctx, tabID)
}

// GetPane reports one pane, resolving a previous identifier through its alias (REQ-WS-007).
func (s *Service) GetPane(ctx context.Context, id string) (domain.Pane, error) {
	if err := checkPaneID(id); err != nil {
		return domain.Pane{}, err
	}
	return s.tree.GetPane(ctx, id)
}

// FocusPane marks a pane focused inside its tab.
func (s *Service) FocusPane(ctx context.Context, id string) (domain.Pane, error) {
	if err := checkPaneID(id); err != nil {
		return domain.Pane{}, err
	}
	pane, err := s.tree.GetPane(ctx, id)
	if err != nil {
		return domain.Pane{}, err
	}
	if err := s.tree.SetFocusedPane(ctx, pane.TabID, pane.ID); err != nil {
		return domain.Pane{}, err
	}
	s.publish(ports.PaneEvent{Kind: ports.KindPaneFocused, Pane: pane})
	return pane, nil
}

// RenamePane changes a pane's label.
func (s *Service) RenamePane(ctx context.Context, id, label string) (domain.Pane, error) {
	if err := checkPaneID(id); err != nil {
		return domain.Pane{}, err
	}
	if err := checkLabel(label); err != nil {
		return domain.Pane{}, err
	}
	pane, err := s.tree.GetPane(ctx, id)
	if err != nil {
		return domain.Pane{}, err
	}
	renamed, err := s.tree.SetPaneLabel(ctx, pane.ID, label)
	if err != nil {
		return domain.Pane{}, err
	}
	s.publish(ports.PaneEvent{Kind: ports.KindPaneUpdated, Pane: renamed})
	return renamed, nil
}

// MovePane is REQ-WS-007. The terminal and its process stay; only the tree around them
// changes, and `pane.moved` is emitted instead of a close and a create so a client can
// follow the pane rather than watch one die and another appear.
func (s *Service) MovePane(ctx context.Context, params domain.MoveParams) (ports.Moved, error) {
	if !params.Destination.Type.Valid() {
		return ports.Moved{}, fmt.Errorf("%w: destination type %q is not one of \"tab\", \"new_tab\" or \"new_workspace\"",
			domain.ErrValidation, params.Destination.Type)
	}
	if err := checkPaneID(params.PaneID); err != nil {
		return ports.Moved{}, err
	}
	switch params.Destination.Type {
	case domain.MoveToTab:
		if err := checkTabID(params.Destination.TabID); err != nil {
			return ports.Moved{}, err
		}
	case domain.MoveToNewTab:
		// The workspace is optional here: an absent one means the pane's own.
		if params.Destination.WorkspaceID != "" {
			if err := checkWorkspaceID(params.Destination.WorkspaceID); err != nil {
				return ports.Moved{}, err
			}
		}
	case domain.MoveToNewWorkspace:
	}
	if err := checkLabel(params.Destination.Label); err != nil {
		return ports.Moved{}, err
	}

	pane, err := s.tree.GetPane(ctx, params.PaneID)
	if err != nil {
		return ports.Moved{}, err
	}
	params.PaneID = pane.ID

	moved, err := s.tree.MovePane(ctx, params)
	if err != nil {
		return ports.Moved{}, err
	}

	// Whatever the move had to build is announced before the move itself, so a client
	// applies `pane.moved` to a tab it already knows about rather than to a reference it
	// has to resolve out of the layout.
	if moved.CreatedWorkspace != nil {
		s.publish(ports.WorkspaceEvent{
			Kind: ports.KindWorkspaceCreated, Workspace: *moved.CreatedWorkspace,
		})
	}
	if moved.CreatedTab != nil {
		s.publish(ports.TabEvent{Kind: ports.KindTabCreated, Tab: *moved.CreatedTab})
	}
	s.publish(ports.PaneMoved{
		Pane:                moved.Pane,
		PreviousPaneID:      moved.PreviousPaneID,
		PreviousWorkspaceID: moved.PreviousWorkspaceID,
		Layout:              moved.Layout,
	})
	// Two trees changed, and `pane.moved` carries only the destination's. A client showing
	// the tab the pane left has to redraw it too.
	s.publish(ports.LayoutUpdated{Layout: moved.SourceLayout})
	s.publish(ports.LayoutUpdated{Layout: moved.Layout})
	return moved, nil
}

// ClosePane closes one pane and terminates its terminal.
func (s *Service) ClosePane(ctx context.Context, id string) error {
	if err := checkPaneID(id); err != nil {
		return err
	}
	pane, err := s.tree.GetPane(ctx, id)
	if err != nil {
		return err
	}
	session, err := s.tree.ClosePane(ctx, pane.ID)
	if err != nil {
		return err
	}
	if session != "" {
		s.closeSessions(ctx, []string{session})
	}
	pane.ClosedAt = closedNow()
	s.publish(ports.PaneEvent{Kind: ports.KindPaneClosed, Pane: pane})
	// The tab's tree lost a node and the split that held it collapsed into its sibling,
	// so a client drawing from the tree needs the new shape and not just the absence.
	s.publishLayout(ctx, pane.TabID)
	return nil
}

// Layout reports a tab's tree.
func (s *Service) Layout(ctx context.Context, tabID string) (domain.Layout, error) {
	if err := checkTabID(tabID); err != nil {
		return domain.Layout{}, err
	}
	return s.tree.Layout(ctx, tabID)
}

// setFocusedWorkspace and setFocusedTab record what the last focus call chose.
func (s *Service) setFocusedWorkspace(ctx context.Context, id string) {
	s.mu.Lock()
	s.focusedWS = id
	tabID := s.focusedTabOf[id]
	s.mu.Unlock()
	s.rememberFocus(ctx, id, tabID)
}

func (s *Service) setFocusedTab(ctx context.Context, workspaceID, tabID string) {
	s.mu.Lock()
	s.focusedWS = workspaceID
	s.focusedTabOf[workspaceID] = tabID
	s.mu.Unlock()
	s.rememberFocus(ctx, workspaceID, tabID)
}

// adoptFocus installs focus read back from the database without writing it again.
func (s *Service) adoptFocus(workspaceID, tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.focusedWS = workspaceID
	if tabID != "" {
		s.focusedTabOf[workspaceID] = tabID
	}
}

// forgetTab and forgetWorkspace drop focus that points at something closed.
//
// Focus is cleared rather than moved to a neighbour. `layout.export` with no `tab_id` then
// answers "no tab is focused" instead of "tab w1:t1 does not exist", which is the truth and
// is something a client can act on; re-pointing would be the daemon guessing which tab the
// user is looking at, and it has no way to know.
func (s *Service) forgetTab(workspaceID, tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.focusedTabOf[workspaceID] == tabID {
		delete(s.focusedTabOf, workspaceID)
	}
}

func (s *Service) forgetWorkspace(workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.focusedTabOf, workspaceID)
	if s.focusedWS == workspaceID {
		s.focusedWS = ""
	}
}

// Focused reports the workspace and tab a client should open on.
func (s *Service) Focused() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.focusedWS, s.focusedTabOf[s.focusedWS]
}

// ExportLayout is REQ-WS-004: a tab's tree, with each pane's label, cwd and launch command.
//
// An empty tabID means the focused tab, which is API Spec §5.7's default. A daemon nothing
// has focused yet has no such tab, and says so rather than picking one: guessing would give
// a client a layout for a tab it is not looking at.
func (s *Service) ExportLayout(ctx context.Context, tabID string) (domain.Layout, error) {
	if tabID == "" {
		if _, tabID = s.Focused(); tabID == "" {
			return domain.Layout{}, fmt.Errorf(
				"%w: no tab is focused, so layout.export needs a tab_id", domain.ErrNotFound)
		}
	}
	return s.Layout(ctx, tabID)
}

// ApplyLayout is REQ-WS-005: a tab that reproduces the structure, labels, cwd, env and
// commands of a portable tree, and a response that says what it did not reproduce.
//
// The tree is validated before anything is written. A layout comes from outside — exported
// months ago, hand-edited, produced by another tool — so it is the one input here that
// cannot be assumed well formed, and a tab half-built from a bad one is worse than a
// refusal: the client believes it has what it drew.
func (s *Service) ApplyLayout(ctx context.Context, params domain.ApplyLayoutParams) (domain.Applied, error) {
	if err := checkWorkspaceID(params.WorkspaceID); err != nil {
		return domain.Applied{}, err
	}
	if err := checkLabel(params.TabLabel); err != nil {
		return domain.Applied{}, err
	}
	if err := params.Root.Validate(0); err != nil {
		return domain.Applied{}, err
	}

	applied, err := s.tree.ApplyLayout(ctx, params)
	if err != nil {
		return domain.Applied{}, err
	}

	// Terminals after the tree, the same order `workspace.create` uses and for the same
	// reason: a terminal started before its row would be orphaned by a failed insert.
	//
	// This is also the step most likely to fail, and the only one the transaction above
	// cannot cover. A layout is portable, so its `cwd` may not exist on this machine and
	// its `command[0]` may not be on this PATH — `Node.Validate` checks the shape, not the
	// world. Returning bare here would leave a committed tab holding some live panes and
	// some empty ones, which is exactly the "asked for four panes and got two" this
	// method exists to prevent. So a failure unwinds: the tab goes, and with it the panes
	// and the terminals that did start.
	//
	// And every pane gets a **shell**, never the command its node carried. REQ-TERM-011:
	// "`layout.apply` behaves the same way: it returns the commands as pending, never as
	// launched". A layout is an intention from another time and possibly another machine,
	// so applying one is not consent to run what it holds; the command is stored, marked
	// pending, and typed at the pane's prompt for the user to accept or edit.
	pendingCommands := false
	for i := range applied.Panes {
		pane, err := s.attachPending(ctx, applied.Panes[i])
		if err != nil {
			s.unapply(ctx, applied)
			return domain.Applied{}, err
		}
		if pane.CommandPending {
			pendingCommands = true
		}
		applied.Panes[i] = pane
	}
	if pendingCommands {
		applied.Warnings = append(applied.Warnings, domain.PendingCommandWarning)
	}

	s.publish(ports.TabEvent{Kind: ports.KindTabCreated, Tab: applied.Tab})
	for _, pane := range applied.Panes {
		s.publish(ports.PaneEvent{Kind: ports.KindPaneCreated, Pane: pane})
	}
	s.publishLayout(ctx, applied.Tab.ID)
	if params.Focus {
		s.setFocusedTab(ctx, applied.Tab.WorkspaceID, applied.Tab.ID)
		s.publish(ports.TabEvent{Kind: ports.KindTabFocused, Tab: applied.Tab})
	}
	return applied, nil
}

// unapply undoes a partly-attached layout.
//
// Nothing is announced: the tab was never announced either, because the notifications for
// an apply come after every terminal is up. From a client's side the call simply failed,
// which is the only thing it can act on.
//
// Failures here are swallowed deliberately. This runs while returning another error — the
// one the caller actually needs — and a cleanup that reported its own would replace the
// cause with a consequence.
func (s *Service) unapply(ctx context.Context, applied domain.Applied) {
	sessions, err := s.tree.CloseTab(ctx, applied.Tab.ID)
	if err != nil {
		return
	}
	s.closeSessions(ctx, sessions)
}

// publishLayout emits §6's `layout.updated` for a tab whose tree changed shape.
//
// It reads the tab back rather than using whatever the caller had in hand, because the
// store is what decides the final shape — a close collapses a split, an apply renumbers
// every pane — and a notification built from the caller's copy would be a guess.
func (s *Service) publishLayout(ctx context.Context, tabID string) {
	layout, err := s.tree.Layout(ctx, tabID)
	if err != nil {
		// The change itself already committed and the caller has been told it succeeded.
		// Failing to describe it is worth no error of its own; the client's recovery is
		// the same either way, which is to ask for the layout.
		return
	}
	s.publish(ports.LayoutUpdated{Layout: layout})
}

// Snapshot assembles the whole tree for `session.snapshot` (REQ-API-001).
//
// It reads workspaces, then each one's tabs, then each tab's panes and layout. The result is
// a flat set of records plus the layouts, which is the shape API Spec §5.3 defines: a client
// keeping a cache wants to replace it wholesale, not to walk a nested document.
//
// No transaction wraps it. A tree that changed underneath is not a problem this method has
// to solve: §5.3 makes the *counter* the boundary, read before any of this, so a client that
// applies the events above it converges on whatever the tree became. A snapshot slightly
// ahead of its own seq is safe; one behind would not be, and that is what the counter's
// read order prevents.
func (s *Service) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return domain.Snapshot{}, err
	}

	out := domain.Snapshot{Workspaces: workspaces}
	workspaceID, tabID := s.Focused()
	out.Focus = domain.Focus{WorkspaceID: workspaceID, TabID: tabID}

	for _, ws := range workspaces {
		tabs, err := s.tree.ListTabs(ctx, ws.ID)
		if err != nil {
			return domain.Snapshot{}, err
		}
		out.Tabs = append(out.Tabs, tabs...)

		for _, tab := range tabs {
			panes, err := s.tree.ListPanes(ctx, tab.ID)
			if err != nil {
				return domain.Snapshot{}, err
			}
			out.Panes = append(out.Panes, panes...)

			layout, err := s.tree.Layout(ctx, tab.ID)
			if err != nil {
				return domain.Snapshot{}, err
			}
			out.Layouts = append(out.Layouts, layout)
		}
	}
	return out, nil
}

// logger returns the configured logger or one that discards, so no call site needs a nil
// check and a test may leave it out.
func logger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}
