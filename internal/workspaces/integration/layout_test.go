package integration_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// TestLayoutExportApplyRoundTrip_REQ_WS_004 is the criterion from both ends: export returns
// the tree with each pane's label, cwd and launch command, and applying that same tree
// rebuilds the structure.
//
// A round trip is the right shape for this test because the two halves can be wrong in ways
// that cancel out. An export that dropped labels and an apply that never read them would
// both pass a one-sided test, so what is asserted is the tree that comes back, node by node,
// against the one that went in.
func TestLayoutExportApplyRoundTrip_REQ_WS_004(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	source := h.newWorkspace(t, "source")

	// Build a tab of three panes with labels, working directories and a command, so the
	// export has something to lose.
	right, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: source.RootPane.ID, Direction: domain.SplitRight, Ratio: 0.6,
	})
	if err != nil {
		t.Fatalf("SplitPane right: %v", err)
	}
	down, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: right.ID, Direction: domain.SplitDown, Ratio: 0.3,
		Command: []string{"sh", "-c", "sleep 30"},
		// REQ-WS-005 names `env` beside labels, cwd and commands, so one pane carries one.
		Env: map[string]string{"UMBRAL_ROLE": "tests"},
	})
	if err != nil {
		t.Fatalf("SplitPane down: %v", err)
	}
	for id, label := range map[string]string{
		source.RootPane.ID: "editor", right.ID: "tests", down.ID: "watch",
	} {
		if _, err := h.RenamePane(t.Context(), id, label); err != nil {
			t.Fatalf("RenamePane(%s): %v", id, err)
		}
	}

	exported, err := h.ExportLayout(t.Context(), source.Tab.ID)
	if err != nil {
		t.Fatalf("ExportLayout: %v", err)
	}
	if exported.Root == nil {
		t.Fatal("the exported layout has no root")
	}
	if got := len(exported.Root.PaneIDs()); got != 3 {
		t.Fatalf("the exported tree has %d panes, want 3", got)
	}

	// REQ-WS-004 names three things each pane node must carry.
	labels := map[string]*domain.Node{}
	for _, leaf := range exported.Root.PaneLeaves() {
		labels[leaf.Label] = leaf
		if leaf.CWD == "" {
			t.Errorf("pane %q was exported with no cwd; REQ-WS-004 names it", leaf.Label)
		}
	}
	for _, want := range []string{"editor", "tests", "watch"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("the export has no pane labelled %q; REQ-WS-004 names labels", want)
		}
	}
	if watch := labels["watch"]; watch != nil {
		if !slices.Equal(watch.Command, []string{"sh", "-c", "sleep 30"}) {
			t.Errorf("the watch pane exported command %v, want the one it was launched with",
				watch.Command)
		}
	}

	// Apply the exported tree into a second workspace.
	destination := h.newWorkspace(t, "destination")
	applied, err := h.ApplyLayout(t.Context(), domain.ApplyLayoutParams{
		WorkspaceID: destination.Workspace.ID, TabLabel: "restored", Root: exported.Root,
	})
	if err != nil {
		t.Fatalf("ApplyLayout: %v", err)
	}
	if len(applied.Panes) != 3 {
		t.Fatalf("the applied tab has %d panes, want 3", len(applied.Panes))
	}

	rebuilt, err := h.ExportLayout(t.Context(), applied.Tab.ID)
	if err != nil {
		t.Fatalf("ExportLayout of the applied tab: %v", err)
	}

	// The structure survived: same shape, same directions, same ratios, same labels and
	// commands in the same positions. The identifiers are deliberately not compared —
	// they belong to the workspace that holds them (REQ-WS-002) and a layout applied into
	// another workspace cannot reuse them.
	if err := sameShape(exported.Root, rebuilt.Root); err != nil {
		t.Errorf("the applied tree differs from the exported one: %v", err)
	}

	// Every pane came back with a terminal of its own, since REQ-WS-005 reproduces the
	// panes and a pane without a shell is not one a user can work in.
	//
	// The command is checked on the pane record and on the launch, not only in the tree.
	// The tree is rebuilt from the nodes that went in, so it would carry a command even if
	// the pane row and the process never saw one — which is exactly what a deliberate
	// break of this proved before these three assertions existed.
	seen := map[string]bool{}
	var relaunched int
	for _, pane := range applied.Panes {
		if pane.SessionID == "" {
			t.Errorf("applied pane %q has no terminal", pane.ID)
		}
		if pane.Label == "watch" {
			relaunched++
			if !slices.Equal(pane.Command, []string{"sh", "-c", "sleep 30"}) {
				t.Errorf("the applied watch pane records command %v, want the exported one",
					pane.Command)
			}
			launched, ok := h.terminals.launchedWith(pane.SessionID)
			if !ok {
				t.Errorf("no terminal was started for pane %q", pane.ID)
			} else if !slices.Equal(launched.Command, []string{"sh", "-c", "sleep 30"}) {
				t.Errorf("the watch pane's terminal was started with %v, want the exported command",
					launched.Command)
			}
			// The env has to reach the process, not merely the row: REQ-WS-005 reproduces
			// it so the relaunched command behaves the way it did before.
			if !maps.Equal(pane.Env, map[string]string{"UMBRAL_ROLE": "tests"}) {
				t.Errorf("the applied watch pane records env %v, want the exported one", pane.Env)
			}
			if ok && launched.Env["UMBRAL_ROLE"] != "tests" {
				t.Errorf("the watch pane's terminal was started with env %v, want UMBRAL_ROLE=tests",
					launched.Env)
			}
			// A pane running a command has nothing to source a bootstrap into.
			if ok && launched.ShellIntegration {
				t.Error("a pane launched with a command asked for shell integration")
			}
		}
		if seen[pane.SessionID] {
			t.Errorf("two applied panes share the terminal %q", pane.SessionID)
		}
		seen[pane.SessionID] = true
		if ws, _, ok := domain.ParsePaneID(pane.ID); !ok || ws != destination.Workspace.ID {
			t.Errorf("applied pane %q is not a pane of %q", pane.ID, destination.Workspace.ID)
		}
	}
	if relaunched != 1 {
		t.Errorf("found %d panes labelled watch among the applied ones, want 1", relaunched)
	}

	// And the row itself, read back rather than taken from the reply.
	for _, pane := range applied.Panes {
		stored, err := h.GetPane(t.Context(), pane.ID)
		if err != nil {
			t.Fatalf("GetPane(%s): %v", pane.ID, err)
		}
		if stored.Label != pane.Label {
			t.Errorf("pane %q was stored with label %q, want %q", pane.ID, stored.Label, pane.Label)
		}
		if !slices.Equal(stored.Command, pane.Command) {
			t.Errorf("pane %q was stored with command %v, want %v", pane.ID, stored.Command, pane.Command)
		}
		// Read back, not taken from the reply: every other assertion about env in this
		// test reads a struct built from the nodes that went in, so an apply that never
		// wrote `env_json` would satisfy all of them. This is the one that touches the row.
		if !maps.Equal(stored.Env, pane.Env) {
			t.Errorf("pane %q was stored with env %v, want %v", pane.ID, stored.Env, pane.Env)
		}
	}
}

// sameShape compares two trees on everything a layout is supposed to carry across, which is
// everything except the identifiers.
func sameShape(want, got *domain.Node) error {
	switch {
	case want == nil && got == nil:
		return nil
	case want == nil || got == nil:
		return errShape("one tree ends where the other does not")
	case want.Type != got.Type:
		return errShape("node type %q became %q", want.Type, got.Type)
	}

	if want.Type == domain.NodePane {
		if want.Label != got.Label {
			return errShape("label %q became %q", want.Label, got.Label)
		}
		if want.CWD != got.CWD {
			return errShape("cwd %q became %q", want.CWD, got.CWD)
		}
		if !slices.Equal(want.Command, got.Command) {
			return errShape("command %v became %v", want.Command, got.Command)
		}
		if !maps.Equal(want.Env, got.Env) {
			return errShape("env %v became %v", want.Env, got.Env)
		}
		return nil
	}

	if want.Direction != got.Direction {
		return errShape("direction %q became %q", want.Direction, got.Direction)
	}
	if want.Ratio != got.Ratio {
		return errShape("ratio %v became %v", want.Ratio, got.Ratio)
	}
	if err := sameShape(want.First, got.First); err != nil {
		return err
	}
	return sameShape(want.Second, got.Second)
}

type shapeError string

func (e shapeError) Error() string { return string(e) }

func errShape(format string, args ...any) error {
	return shapeError(sprintf(format, args...))
}

// TestApplyWarnsNoProcesses_REQ_WS_005 is the half of the requirement that is about what
// `layout.apply` does *not* do.
//
// A layout carries structure, not state. The panes come back, the commands are relaunched,
// and everything that was on the screen — the half-finished build, the scrollback, the
// process that was running — does not. REQ-WS-005 makes saying so part of the response
// rather than something a client is expected to know, and this pins the sentence.
func TestApplyWarnsNoProcesses_REQ_WS_005(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ws := h.newWorkspace(t, "w")

	applied, err := h.ApplyLayout(t.Context(), domain.ApplyLayoutParams{
		WorkspaceID: ws.Workspace.ID, TabLabel: "restored",
		Root: &domain.Node{
			Type: domain.NodeSplit, Direction: domain.SplitRight, Ratio: 0.5,
			First:  &domain.Node{Type: domain.NodePane, Label: "a", CWD: "/tmp"},
			Second: &domain.Node{Type: domain.NodePane, Label: "b", CWD: "/tmp"},
		},
	})
	if err != nil {
		t.Fatalf("ApplyLayout: %v", err)
	}

	if len(applied.Warnings) == 0 {
		t.Fatal("the response carries no warnings; REQ-WS-005 requires it to state what was not reproduced")
	}
	if !slices.Contains(applied.Warnings, domain.ApplyWarning) {
		t.Errorf("warnings = %v, want them to include %q", applied.Warnings, domain.ApplyWarning)
	}
	// The sentence is the contract, not a paraphrase: a client shows it to a person.
	for _, word := range []string{"processes", "scrollback", "not reproduced"} {
		if !strings.Contains(domain.ApplyWarning, word) {
			t.Errorf("the warning %q does not mention %q", domain.ApplyWarning, word)
		}
	}
}

// TestApplyRefusesAMalformedTree: a layout comes from outside, so it is the one tree here
// that cannot be assumed well formed. A bad one must leave nothing behind — a client told
// its layout failed and then finding half a tab would have no way to clean up.
func TestApplyRefusesAMalformedTree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ws := h.newWorkspace(t, "w")

	before, err := h.ListTabs(t.Context(), ws.Workspace.ID)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}

	cases := map[string]*domain.Node{
		"no root": nil,
		"a split with one child": {
			Type: domain.NodeSplit, Direction: domain.SplitRight,
			First: &domain.Node{Type: domain.NodePane, CWD: "/tmp"},
		},
		"a split going nowhere": {
			Type: domain.NodeSplit, Direction: "sideways",
			First:  &domain.Node{Type: domain.NodePane, CWD: "/tmp"},
			Second: &domain.Node{Type: domain.NodePane, CWD: "/tmp"},
		},
		"a node of no known type": {Type: "window"},
		"a command naming no program": {
			Type: domain.NodePane, CWD: "/tmp", Command: []string{""},
		},
		"a command with more arguments than the cap": {
			Type: domain.NodePane, CWD: "/tmp",
			Command: append([]string{"sh"}, make([]string, domain.MaxCommandArgs)...),
		},
		"a tree nested deeper than the cap": deepTree(domain.MaxLayoutDepth + 2),
	}
	for name, root := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.ApplyLayout(t.Context(), domain.ApplyLayoutParams{
				WorkspaceID: ws.Workspace.ID, TabLabel: "bad", Root: root,
			})
			if err == nil {
				t.Fatal("the malformed layout was applied")
			}
			if !isValidation(err) {
				t.Errorf("error = %v, want a validation error", err)
			}
		})
	}

	after, err := h.ListTabs(t.Context(), ws.Workspace.ID)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the workspace has %d tabs after %d refused applies, want %d",
			len(after), len(cases), len(before))
	}
}

// TestExportDefaultsToTheFocusedTab covers API Spec §5.7's optional `tab_id`, which is what
// lets a client bind export to a key without tracking which tab it is on.
func TestExportDefaultsToTheFocusedTab(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// Nothing focused yet: the daemon says so rather than guessing a tab.
	if _, err := h.ExportLayout(t.Context(), ""); err == nil {
		t.Error("export with no tab_id succeeded before anything was focused")
	}

	first := h.newWorkspace(t, "first")
	second := h.newWorkspace(t, "second")

	// newWorkspace focuses what it creates, so the second is current.
	layout, err := h.ExportLayout(t.Context(), "")
	if err != nil {
		t.Fatalf("ExportLayout with no tab_id: %v", err)
	}
	if layout.TabID != second.Tab.ID {
		t.Errorf("exported %q, want the focused tab %q", layout.TabID, second.Tab.ID)
	}

	if _, err := h.FocusTab(t.Context(), first.Tab.ID); err != nil {
		t.Fatalf("FocusTab: %v", err)
	}
	layout, err = h.ExportLayout(t.Context(), "")
	if err != nil {
		t.Fatalf("ExportLayout after focusing: %v", err)
	}
	if layout.TabID != first.Tab.ID {
		t.Errorf("exported %q after focusing %q", layout.TabID, first.Tab.ID)
	}
}

// TestApplyUnwindsWhenATerminalWillNotStart is finding 3 of the T-F0-15 review, and the one
// case `treestore.ApplyLayout`'s transaction cannot cover.
//
// A layout is portable, which is another way of saying it describes a machine that may not
// be this one: a `cwd` that does not exist here, a `command[0]` that is not on this PATH.
// `Node.Validate` checks the shape, not the world, so the first thing that touches the world
// is the terminal — after the rows have committed. Returning bare there would leave a tab
// holding some live panes and some empty ones, which is precisely the "asked for four panes
// and got two" the method's own comment says cannot happen.
func TestApplyUnwindsWhenATerminalWillNotStart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ws := h.newWorkspace(t, "w")
	tabsBefore, err := h.ListTabs(t.Context(), ws.Workspace.ID)
	if err != nil {
		t.Fatalf("ListTabs: %v", err)
	}
	startedBefore := len(h.terminals.createdIDs())

	// The second pane's terminal refuses. The first one's has already started.
	h.terminals.failAfter(1)

	_, err = h.ApplyLayout(t.Context(), domain.ApplyLayoutParams{
		WorkspaceID: ws.Workspace.ID, TabLabel: "doomed",
		Root: &domain.Node{
			Type: domain.NodeSplit, Direction: domain.SplitRight, Ratio: 0.5,
			First:  &domain.Node{Type: domain.NodePane, Label: "a", CWD: "/tmp"},
			Second: &domain.Node{Type: domain.NodePane, Label: "b", CWD: "/tmp"},
		},
	})
	if err == nil {
		t.Fatal("the apply succeeded although a terminal refused to start")
	}

	tabsAfter, err := h.ListTabs(t.Context(), ws.Workspace.ID)
	if err != nil {
		t.Fatalf("ListTabs after the failure: %v", err)
	}
	if len(tabsAfter) != len(tabsBefore) {
		t.Errorf("the workspace has %d tabs after a failed apply, want %d: the half-built tab was left behind",
			len(tabsAfter), len(tabsBefore))
	}

	// The terminal that did start was closed rather than orphaned. Nothing points at it
	// any more, so nobody would ever close it.
	started := h.terminals.createdIDs()[startedBefore:]
	if len(started) != 1 {
		t.Fatalf("%d terminals started before the failure, want 1", len(started))
	}
	if !slices.Contains(h.terminals.closedIDs(), started[0]) {
		t.Errorf("terminal %q was started and then orphaned by the failed apply", started[0])
	}
}

// TestTreeChangesAnnounceTheLayout_REQ_WS_004 pins §6's `layout.updated`, which T-F0-14 did
// not emit at all.
//
// A client that draws from the tree needs its new shape, and reconstructing it from the pane
// events is work the daemon has already done: a close collapses the split that held the
// pane into its sibling, and nothing in `pane.closed` says which sibling grew.
func TestTreeChangesAnnounceTheLayout_REQ_WS_004(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ws := h.newWorkspace(t, "w")
	other := h.newWorkspace(t, "other")
	h.drain()

	pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: ws.RootPane.ID, Direction: domain.SplitRight,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if n := countKind(h.drain(), wsports.KindLayoutUpdated); n != 1 {
		t.Errorf("a split published %d layout.updated, want 1", n)
	}

	// A move changes two trees: the one the pane left and the one it joined. `pane.moved`
	// carries only the destination's.
	if _, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID:      pane.ID,
		Destination: domain.MoveDestination{Type: domain.MoveToTab, TabID: other.Tab.ID},
	}); err != nil {
		t.Fatalf("MovePane: %v", err)
	}
	if n := countKind(h.drain(), wsports.KindLayoutUpdated); n != 2 {
		t.Errorf("a move published %d layout.updated, want 2 — one per tree", n)
	}

	applied, err := h.ApplyLayout(t.Context(), domain.ApplyLayoutParams{
		WorkspaceID: ws.Workspace.ID, TabLabel: "applied",
		Root: &domain.Node{Type: domain.NodePane, Label: "only", CWD: "/tmp"},
	})
	if err != nil {
		t.Fatalf("ApplyLayout: %v", err)
	}
	events := h.drain()
	if n := countKind(events, wsports.KindLayoutUpdated); n != 1 {
		t.Errorf("an apply published %d layout.updated, want 1; kinds were %v", n, kindsOf(events))
	}

	if err := h.ClosePane(t.Context(), applied.Panes[0].ID); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if n := countKind(h.drain(), wsports.KindLayoutUpdated); n != 1 {
		t.Errorf("a close published %d layout.updated, want 1", n)
	}
}

// TestFocusIsForgottenWhenItsTargetCloses is finding 5 of the review.
//
// `layout.export` with no `tab_id` means "the tab I am looking at". After that tab closes
// there is no such tab, and the honest answer says so — rather than naming a tab the client
// never mentioned and reporting it missing, which reads like a different bug entirely.
func TestFocusIsForgottenWhenItsTargetCloses(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.newWorkspace(t, "first")

	if _, err := h.ExportLayout(t.Context(), ""); err != nil {
		t.Fatalf("ExportLayout while focused: %v", err)
	}

	second, _, err := h.CreateTab(t.Context(), first.Workspace.ID, "second", true)
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if err := h.CloseTab(t.Context(), second.ID); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	_, err = h.ExportLayout(t.Context(), "")
	if err == nil {
		t.Fatal("export with no tab_id succeeded after the focused tab closed")
	}
	if !strings.Contains(err.Error(), "no tab is focused") {
		t.Errorf("error = %v, want it to say no tab is focused rather than naming a missing tab", err)
	}

	// The same for a whole workspace.
	third := h.newWorkspace(t, "third")
	if err := h.CloseWorkspace(t.Context(), third.Workspace.ID, true); err != nil {
		t.Fatalf("CloseWorkspace: %v", err)
	}
	if _, err := h.ExportLayout(t.Context(), ""); err == nil {
		t.Fatal("export with no tab_id succeeded after the focused workspace closed")
	}
}

// deepTree builds a right-leaning tree of the given depth, for the one bound that cannot be
// reached by writing the literal out.
func deepTree(depth int) *domain.Node {
	node := &domain.Node{Type: domain.NodePane, CWD: "/tmp"}
	for range depth {
		node = &domain.Node{
			Type: domain.NodeSplit, Direction: domain.SplitRight, Ratio: 0.5,
			First:  &domain.Node{Type: domain.NodePane, CWD: "/tmp"},
			Second: node,
		}
	}
	return node
}

// TestTheTwoCommandCapsAgree: a layout that passed the tree's cap and then failed the
// sessions module's identical one would be refused after its tab had been built, which is
// the failure `ApplyLayout` goes to some length to avoid. They are one constant now, and
// this is what says so out loud if someone splits them again.
func TestTheTwoCommandCapsAgree(t *testing.T) {
	t.Parallel()

	if domain.MaxCommandArgs != sessdomain.MaxCommandArgs {
		t.Errorf("the layout caps a command at %d arguments and the sessions module at %d; "+
			"a layout between the two would be accepted here and refused at the launch",
			domain.MaxCommandArgs, sessdomain.MaxCommandArgs)
	}
}
