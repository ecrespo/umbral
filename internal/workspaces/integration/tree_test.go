package integration_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestWorkspaceCreateReturnsTree_REQ_WS_001 is the criterion in its own words: one call
// creates the workspace *together with* its first tab and its root pane, and returns the
// three records in a single response.
//
// The three-in-one shape is the whole requirement. A client that had to call three times
// could observe a workspace with no tab, and would have to decide what to draw for it.
func TestWorkspaceCreateReturnsTree_REQ_WS_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	cwd := t.TempDir()
	tree, err := h.CreateWorkspace(t.Context(), domain.CreateWorkspaceParams{
		CWD: cwd, Label: "api", TabLabel: "agents",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	if tree.Workspace.ID != "w1" {
		t.Errorf("workspace id = %q, want w1", tree.Workspace.ID)
	}
	if tree.Tab.ID != "w1:t1" {
		t.Errorf("tab id = %q, want w1:t1", tree.Tab.ID)
	}
	if tree.RootPane.ID != "w1:p1" {
		t.Errorf("root pane id = %q, want w1:p1", tree.RootPane.ID)
	}

	// The three have to be joined up, not merely present: a tab that named another
	// workspace would satisfy "three records" and nothing else.
	if tree.Tab.WorkspaceID != tree.Workspace.ID {
		t.Errorf("tab belongs to %q, want %q", tree.Tab.WorkspaceID, tree.Workspace.ID)
	}
	if tree.RootPane.TabID != tree.Tab.ID {
		t.Errorf("pane belongs to tab %q, want %q", tree.RootPane.TabID, tree.Tab.ID)
	}
	if !slices.Contains(tree.Workspace.TabIDs, tree.Tab.ID) {
		t.Errorf("workspace lists tabs %v, which does not include %q", tree.Workspace.TabIDs, tree.Tab.ID)
	}
	if tree.Tab.FocusedPaneID != tree.RootPane.ID {
		t.Errorf("tab focuses %q, want the root pane %q", tree.Tab.FocusedPaneID, tree.RootPane.ID)
	}

	// The root pane arrives with a terminal. A pane a user cannot type into is not a
	// workspace they can work in.
	if tree.RootPane.SessionID == "" {
		t.Error("the root pane has no session")
	}
	if created := h.terminals.createdIDs(); len(created) != 1 {
		t.Errorf("terminals created = %v, want exactly one", created)
	}

	// REQ-WS-006 and delta `2026-09-pane-attention-state`: a fresh workspace holds a pane
	// nothing has reported on, so it is `unknown` — not `idle`, which would claim the
	// daemon knows the workspace is quiet.
	if tree.Workspace.RollupState != domain.AttentionUnknown {
		t.Errorf("rollup = %q, want %q for a workspace of unreported panes",
			tree.Workspace.RollupState, domain.AttentionUnknown)
	}

	// And it is all there after the call, not only in the reply.
	listed, err := h.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != tree.Workspace.ID {
		t.Fatalf("ListWorkspaces = %+v, want the workspace just created", listed)
	}
	if listed[0].RollupState != domain.AttentionUnknown {
		t.Errorf("listed rollup = %q, want %q", listed[0].RollupState, domain.AttentionUnknown)
	}
}

// TestPaneIdsStable_REQ_WS_002 covers the half of the requirement a grammar test cannot:
// that the identifiers are unique and stable *while the object exists*, and that one is
// never handed out twice.
func TestPaneIdsStable_REQ_WS_002(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.newWorkspace(t, "one")
	second := h.newWorkspace(t, "two")

	if second.Workspace.ID != "w2" {
		t.Errorf("the second workspace is %q, want w2", second.Workspace.ID)
	}
	// Pane numbers are scoped to the workspace, so the second workspace starts at p1 again
	// and the two are still different strings.
	if second.RootPane.ID != "w2:p1" {
		t.Errorf("the second workspace's root pane is %q, want w2:p1", second.RootPane.ID)
	}

	seen := map[string]bool{first.RootPane.ID: true, second.RootPane.ID: true}
	previous := first.RootPane.ID
	for i := range 5 {
		pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{
			PaneID: previous, Direction: domain.SplitRight,
		})
		if err != nil {
			t.Fatalf("split %d: %v", i, err)
		}
		if seen[pane.ID] {
			t.Fatalf("split %d reused the identifier %q", i, pane.ID)
		}
		seen[pane.ID] = true

		if ws, _, ok := domain.ParsePaneID(pane.ID); !ok || ws != first.Workspace.ID {
			t.Errorf("pane %q is not a pane of %q", pane.ID, first.Workspace.ID)
		}
		previous = pane.ID
	}

	// Stable means the identifier a pane was given still reaches the same pane later, not
	// merely that it parses.
	reread, err := h.GetPane(t.Context(), first.RootPane.ID)
	if err != nil {
		t.Fatalf("GetPane(%q): %v", first.RootPane.ID, err)
	}
	if reread.ID != first.RootPane.ID {
		t.Errorf("GetPane(%q) returned %q", first.RootPane.ID, reread.ID)
	}

	// Closing a pane does not release its number: the row stays for the retention window
	// (Data Model §7) and anything still holding the string must not be sent to a
	// different pane.
	closed := previous
	if err := h.ClosePane(t.Context(), closed); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	after, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: first.RootPane.ID, Direction: domain.SplitDown,
	})
	if err != nil {
		t.Fatalf("split after close: %v", err)
	}
	if after.ID == closed {
		t.Errorf("the identifier %q was handed out again after its pane closed", closed)
	}
}

// TestSplitAttachesSession_REQ_WS_003 is the criterion: a split creates the pane in the
// given position, attaches a terminal to it, and returns the pane and the layout.
func TestSplitAttachesSession_REQ_WS_003(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tree := h.newWorkspace(t, "repo")
	before := len(h.terminals.createdIDs())

	pane, layout, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: domain.SplitRight, Ratio: 0.6,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}

	if pane.SessionID == "" {
		t.Error("the new pane has no terminal attached")
	}
	if got := len(h.terminals.createdIDs()); got != before+1 {
		t.Errorf("terminals created = %d, want %d", got, before+1)
	}
	if pane.SessionID == tree.RootPane.SessionID {
		t.Error("the new pane shares the original pane's terminal")
	}

	// The position: the root is now a split, the original pane is first and the new one
	// second, with the ratio that was asked for.
	if layout.Root == nil || layout.Root.Type != domain.NodeSplit {
		t.Fatalf("the layout root is %+v, want a split", layout.Root)
	}
	if layout.Root.Direction != domain.SplitRight {
		t.Errorf("split direction = %q, want right", layout.Root.Direction)
	}
	if layout.Root.Ratio != 0.6 {
		t.Errorf("split ratio = %v, want 0.6", layout.Root.Ratio)
	}
	if layout.Root.First.PaneID != tree.RootPane.ID {
		t.Errorf("first = %q, want the pane that was split, %q", layout.Root.First.PaneID, tree.RootPane.ID)
	}
	if layout.Root.Second.PaneID != pane.ID {
		t.Errorf("second = %q, want the new pane %q", layout.Root.Second.PaneID, pane.ID)
	}

	// A split inherits the cwd of the pane it came from, which is what makes splitting a
	// shell in a repository useful.
	if pane.CWD != tree.RootPane.CWD {
		t.Errorf("the new pane's cwd is %q, want the original's %q", pane.CWD, tree.RootPane.CWD)
	}

	// The layout is what `pane.list` orders by, so both panes are there and in tree order.
	panes, err := h.ListPanes(t.Context(), tree.Tab.ID)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 2 || panes[0].ID != tree.RootPane.ID || panes[1].ID != pane.ID {
		ids := make([]string, len(panes))
		for i, p := range panes {
			ids[i] = p.ID
		}
		t.Errorf("panes = %v, want [%s %s]", ids, tree.RootPane.ID, pane.ID)
	}

	// A ratio outside §5.6's bounds is clamped rather than refused: a client dragging a
	// divider to the edge should get the edge, not an error.
	_, wide, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: pane.ID, Direction: domain.SplitDown, Ratio: 5,
	})
	if err != nil {
		t.Fatalf("split with an out-of-range ratio: %v", err)
	}
	if got := deepest(wide.Root).Ratio; got != domain.MaxSplitRatio {
		t.Errorf("ratio 5 became %v, want it clamped to %v", got, domain.MaxSplitRatio)
	}
}

// deepest returns the last split on the right spine, which is where the most recent split
// of the second child lands.
func deepest(n *domain.Node) *domain.Node {
	last := n
	for n != nil && n.Type == domain.NodeSplit {
		last = n
		n = n.Second
	}
	return last
}

// TestMovedPaneKeepsAlias_REQ_WS_007 is the unwanted-behaviour criterion: a pane moved to
// another workspace gets a new identifier, the previous one stays resolvable for the life
// of the terminal, and `pane.moved` is emitted instead of a close and create pair.
func TestMovedPaneKeepsAlias_REQ_WS_007(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	source := h.newWorkspace(t, "source")
	destination := h.newWorkspace(t, "destination")

	pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: source.RootPane.ID, Direction: domain.SplitRight,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	original := pane.ID
	terminal := pane.SessionID
	closedBefore := len(h.terminals.closedIDs())

	moved, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID: original,
		Destination: domain.MoveDestination{
			Type: domain.MoveToTab, TabID: destination.Tab.ID,
		},
	})
	if err != nil {
		t.Fatalf("MovePane: %v", err)
	}

	if moved.Pane.ID == original {
		t.Errorf("the pane kept %q after moving workspace; REQ-WS-007 requires a new identifier", original)
	}
	if ws, _, ok := domain.ParsePaneID(moved.Pane.ID); !ok || ws != destination.Workspace.ID {
		t.Errorf("the moved pane is %q, want one of %q", moved.Pane.ID, destination.Workspace.ID)
	}
	if moved.PreviousPaneID != original {
		t.Errorf("previous_pane_id = %q, want %q", moved.PreviousPaneID, original)
	}
	if moved.PreviousWorkspaceID != source.Workspace.ID {
		t.Errorf("previous_workspace_id = %q, want %q", moved.PreviousWorkspaceID, source.Workspace.ID)
	}

	// The terminal and its process stay. A move that closed one would be the close/create
	// pair the requirement forbids, wearing a different name.
	if moved.Pane.SessionID != terminal {
		t.Errorf("the moved pane's terminal is %q, want the original %q", moved.Pane.SessionID, terminal)
	}
	if closed := h.terminals.closedIDs(); len(closed) != closedBefore {
		t.Errorf("moving closed terminals %v; the pane is supposed to keep its process", closed[closedBefore:])
	}

	// The old identifier still reaches the pane, which is what "resolvable as an alias"
	// buys: a script or a client holding `w1:p2` is not broken by someone dragging a pane.
	byAlias, err := h.GetPane(t.Context(), original)
	if err != nil {
		t.Fatalf("GetPane(%q) after the move: %v", original, err)
	}
	if byAlias.ID != moved.Pane.ID {
		t.Errorf("the old identifier resolved to %q, want %q", byAlias.ID, moved.Pane.ID)
	}
	if !slices.Contains(byAlias.Aliases, original) {
		t.Errorf("aliases = %v, which does not include the previous identifier %q", byAlias.Aliases, original)
	}
	if !slices.Contains(byAlias.Aliases, moved.Pane.ID) {
		t.Errorf("aliases = %v, which does not include the current identifier %q", byAlias.Aliases, moved.Pane.ID)
	}

	// Both trees changed: the pane left one and joined the other.
	if slices.Contains(moved.SourceLayout.Root.PaneIDs(), original) {
		t.Errorf("the source layout still holds %q", original)
	}
	if !slices.Contains(moved.Layout.Root.PaneIDs(), moved.Pane.ID) {
		t.Errorf("the destination layout %v does not hold %q", moved.Layout.Root.PaneIDs(), moved.Pane.ID)
	}

	// Moving twice keeps every name it has ever had, flat rather than chained.
	again, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID: moved.Pane.ID,
		Destination: domain.MoveDestination{
			Type: domain.MoveToNewWorkspace, Label: "third",
		},
	})
	if err != nil {
		t.Fatalf("second MovePane: %v", err)
	}
	for _, name := range []string{original, moved.Pane.ID} {
		found, err := h.GetPane(t.Context(), name)
		if err != nil {
			t.Errorf("GetPane(%q) after two moves: %v", name, err)
			continue
		}
		if found.ID != again.Pane.ID {
			t.Errorf("%q resolved to %q, want %q", name, found.ID, again.Pane.ID)
		}
	}
}

// TestUnknownIdentifiersAreNotFound pins the boundary the API's error table depends on: a
// name that is merely wrong must be NOT_FOUND, not a nil pane nobody checks.
func TestUnknownIdentifiersAreNotFound(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.newWorkspace(t, "one")

	for _, id := range []string{"w9", "w9:t1", "w1:p99"} {
		var err error
		switch {
		case strings.Contains(id, ":p"):
			_, err = h.GetPane(t.Context(), id)
		case strings.Contains(id, ":t"):
			_, err = h.ListPanes(t.Context(), id)
		default:
			_, err = h.RenameWorkspace(t.Context(), id, "x")
		}
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%q gave %v, want ErrNotFound", id, err)
		}
	}
}

// TestANewPaneNeverTakesAnAliasedName_REQ_WS_002 covers the collision the schema cannot.
//
// `panes.id` and `pane_aliases.alias_id` are primary keys of *different* tables, so nothing
// stops a new pane being called `w1:p2` while an alias of that name still points at the
// pane that used to be. If that happened, `GetPane("w1:p2")` would answer with whichever
// table it read first, and REQ-WS-002's "unique within a session" would be false for the
// one identifier a user is most likely to have written down.
//
// The allocator is the only place that can prevent it, which is why this test exists: the
// rest of the suite would pass with the alias half of that query deleted.
func TestANewPaneNeverTakesAnAliasedName_REQ_WS_002(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	source := h.newWorkspace(t, "source")
	destination := h.newWorkspace(t, "destination")

	// Move w1:p2 out of w1, leaving `w1:p2` behind as an alias.
	pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: source.RootPane.ID, Direction: domain.SplitRight,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	vacated := pane.ID
	moved, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID:      vacated,
		Destination: domain.MoveDestination{Type: domain.MoveToTab, TabID: destination.Tab.ID},
	})
	if err != nil {
		t.Fatalf("MovePane: %v", err)
	}

	// Now make a new pane in the workspace the old name belongs to.
	fresh, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: source.RootPane.ID, Direction: domain.SplitDown,
	})
	if err != nil {
		t.Fatalf("SplitPane after the move: %v", err)
	}
	if fresh.ID == vacated {
		t.Fatalf("the new pane took %q, which is still an alias of %q", vacated, moved.Pane.ID)
	}

	// And the name still means what it meant: the pane that moved, not the one just made.
	resolved, err := h.GetPane(t.Context(), vacated)
	if err != nil {
		t.Fatalf("GetPane(%q): %v", vacated, err)
	}
	if resolved.ID != moved.Pane.ID {
		t.Errorf("%q resolved to %q, want the moved pane %q", vacated, resolved.ID, moved.Pane.ID)
	}
}

// TestConcurrentCreatesDoNotCollideOrFail_REQ_WS_002 puts eight clients on the tree at once.
//
// It exists because the first version of the allocator passed every other test and failed
// five times out of eight here. Allocating an identifier is a read — `max(...)` over the
// existing rows — followed by the insert that uses it, and SQLite's default BEGIN DEFERRED
// starts such a transaction as a reader and only takes the write lock at the insert. Two of
// them cannot both finish: the loser gets SQLITE_BUSY_SNAPSHOT, which `busy_timeout` does
// not wait out because its snapshot is already stale. The cure is BEGIN IMMEDIATE, set in
// the DSN, and this is what proves it is still set.
//
// The two properties are separate and both matter. Uniqueness alone was never in danger —
// the failures were clean rejections, not duplicate names. What was in danger was the write
// happening at all, which is why the count is asserted as loudly as the identifiers.
func TestConcurrentCreatesDoNotCollideOrFail_REQ_WS_002(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	const clients = 8

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		ids    []string
		failed []error
	)
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tree, err := h.CreateWorkspace(t.Context(), domain.CreateWorkspaceParams{
				CWD: "/tmp", Label: "concurrent", TabLabel: "concurrent",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, err)
				return
			}
			ids = append(ids, tree.Workspace.ID)
		}()
	}
	wg.Wait()

	for _, err := range failed {
		t.Errorf("a concurrent workspace.create failed: %v", err)
	}
	if len(ids) != clients {
		t.Fatalf("%d of %d creates succeeded", len(ids), clients)
	}

	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Errorf("two workspaces were both called %q", id)
		}
		seen[id] = true
	}
}

// TestMoveWithinAWorkspaceKeepsTheName_REQ_WS_007 covers the other half of the requirement
// and the destination type nothing else reaches.
//
// REQ-WS-007 renames a pane moved "to another workspace". A move between two tabs of the
// *same* workspace is not that case, and the identifier grammar is why: `w<n>:p<m>` names a
// workspace and a pane and says nothing about a tab, so there is no name to change. A
// client holding `w1:p2` keeps working and no alias is spent.
//
// It also pins something the `new_tab` destination could get wrong invisibly. A new tab is
// created with a root pane, because `tab.create` must never hand back an empty one; the
// move then closes that placeholder and puts the arriving pane in its place. The layout
// alone would not catch a failure to close it — the layout is rewritten wholesale — so the
// check is on `pane.list`, which reads the rows: a placeholder left open is a pane in the
// new tab that the user never asked for and the layout cannot draw.
func TestMoveWithinAWorkspaceKeepsTheName_REQ_WS_007(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ws := h.newWorkspace(t, "one")
	pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: ws.RootPane.ID, Direction: domain.SplitRight,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	terminalsBefore := len(h.terminals.createdIDs())

	moved, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID: pane.ID,
		Destination: domain.MoveDestination{
			Type: domain.MoveToNewTab, WorkspaceID: ws.Workspace.ID, Label: "moved",
		},
	})
	if err != nil {
		t.Fatalf("MovePane to a new tab: %v", err)
	}

	if moved.Pane.ID != pane.ID {
		t.Errorf("the pane was renamed to %q by a move inside %q; only crossing a workspace renames",
			moved.Pane.ID, ws.Workspace.ID)
	}
	if moved.Pane.TabID == pane.TabID {
		t.Errorf("the pane is still in tab %q; it was supposed to move to a new one", moved.Pane.TabID)
	}
	if moved.Pane.SessionID != pane.SessionID {
		t.Errorf("the terminal changed from %q to %q", pane.SessionID, moved.Pane.SessionID)
	}
	if got := len(h.terminals.createdIDs()); got != terminalsBefore {
		t.Errorf("the move started %d terminal(s); the placeholder pane must never get one",
			got-terminalsBefore)
	}

	// The new tab holds exactly the arriving pane, and focuses it. Both the layout and the
	// rows are checked: the layout is rewritten wholesale by the move, so it would look
	// right even if the placeholder row survived underneath it.
	if ids := moved.Layout.Root.PaneIDs(); len(ids) != 1 || ids[0] != pane.ID {
		t.Errorf("the new tab holds %v, want just %q", ids, pane.ID)
	}
	arrived, err := h.ListPanes(t.Context(), moved.Pane.TabID)
	if err != nil {
		t.Fatalf("ListPanes(%q): %v", moved.Pane.TabID, err)
	}
	if len(arrived) != 1 || arrived[0].ID != pane.ID {
		ids := make([]string, len(arrived))
		for i, p := range arrived {
			ids[i] = p.ID
		}
		t.Errorf("the new tab's panes are %v, want just %q: the placeholder was not closed",
			ids, pane.ID)
	}
	if moved.Layout.FocusedPaneID != pane.ID {
		t.Errorf("the new tab focuses %q, want %q", moved.Layout.FocusedPaneID, pane.ID)
	}

	// And the tab it left is back to one pane, focused.
	remaining, err := h.ListPanes(t.Context(), ws.Tab.ID)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != ws.RootPane.ID {
		t.Errorf("the source tab holds %d panes, want just the root", len(remaining))
	}
}
