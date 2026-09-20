package integration_test

import (
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestSnapshotReturnsEveryPaneAndLayout_REQ_API_001 covers the half of `session.snapshot`
// that the wire tests cannot reach.
//
// API Spec §5.3 promises "the workspace, tab, pane and layout records", and `internal/api`'s
// tests drive a fake tree that returns canned slices — so they prove the wire shape and
// nothing about the assembly. A `Snapshot` that returned only the focused tab's panes, or one
// layout for every tab, would satisfy every one of them. This one runs the real service over
// a real database and asserts the whole tree comes back.
func TestSnapshotReturnsEveryPaneAndLayout_REQ_API_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// Two workspaces, two tabs each, and a split in one tab: enough that "returned the
	// focused tab" and "returned one layout" are both visibly wrong.
	first := h.newWorkspace(t, "first")
	second := h.newWorkspace(t, "second")

	type wantTab struct {
		id    string
		panes int
	}
	want := make([]wantTab, 0, 4)
	want = append(want, wantTab{id: first.Tab.ID, panes: 2}, wantTab{id: second.Tab.ID, panes: 1})

	for _, tree := range []domain.Tree{first, second} {
		tab, _, err := h.CreateTab(t.Context(), tree.Workspace.ID, "extra", false)
		if err != nil {
			t.Fatalf("CreateTab on %s: %v", tree.Workspace.ID, err)
		}
		want = append(want, wantTab{id: tab.ID, panes: 1})
	}

	// The split lands in the first workspace's original tab, so exactly one tab has two
	// panes and a layout tree deeper than a single leaf.
	if _, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: first.RootPane.ID, Direction: domain.SplitRight, Ratio: 0.5, CWD: t.TempDir(),
	}); err != nil {
		t.Fatalf("SplitPane: %v", err)
	}

	snapshot, err := h.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if len(snapshot.Workspaces) != 2 {
		t.Errorf("snapshot holds %d workspaces, want 2", len(snapshot.Workspaces))
	}
	if len(snapshot.Tabs) != len(want) {
		t.Errorf("snapshot holds %d tabs, want %d", len(snapshot.Tabs), len(want))
	}

	// One layout per tab, and the panes of every tab — not just the focused one.
	for _, w := range want {
		panes := 0
		for _, p := range snapshot.Panes {
			if p.TabID == w.id {
				panes++
			}
		}
		if panes != w.panes {
			t.Errorf("tab %s contributes %d panes to the snapshot, want %d", w.id, panes, w.panes)
		}

		layouts := 0
		for _, l := range snapshot.Layouts {
			if l.TabID == w.id {
				layouts++
				if l.Root == nil {
					t.Errorf("tab %s has a layout with no root", w.id)
				}
			}
		}
		if layouts != 1 {
			t.Errorf("tab %s has %d layouts in the snapshot, want exactly 1", w.id, layouts)
		}
	}

	// Every pane carries the terminal it was attached to: a snapshot that lost the
	// session id would leave a client unable to subscribe to anything it restored.
	for _, p := range snapshot.Panes {
		if p.SessionID == "" {
			t.Errorf("pane %s comes back with no session_id", p.ID)
		}
		if !slices.Contains(h.terminals.createdIDs(), p.SessionID) {
			t.Errorf("pane %s names session %q, which was never created", p.ID, p.SessionID)
		}
	}

	// Focus is the service's own, not a value re-derived from the rows.
	wsID, tabID := h.Focused()
	if snapshot.Focus.WorkspaceID != wsID || snapshot.Focus.TabID != tabID {
		t.Errorf("snapshot focus = {%q, %q}, want {%q, %q}",
			snapshot.Focus.WorkspaceID, snapshot.Focus.TabID, wsID, tabID)
	}
}

// TestSnapshotFocusIsEmptyOnAFreshDaemon_REQ_API_001 pins the state every client meets first.
//
// Before the first `workspace.create` nothing is focused, and §5.3 types all three members of
// `focused` as nullable for exactly this moment. The service reports it as empty strings,
// which `internal/api` maps to JSON null; what matters here is that it is reported rather
// than invented, and that the snapshot is a valid empty tree instead of an error.
func TestSnapshotFocusIsEmptyOnAFreshDaemon_REQ_API_001(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	snapshot, err := h.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot on a fresh daemon: %v", err)
	}

	if snapshot.Focus != (domain.Focus{}) {
		t.Errorf("focus = %+v on a daemon with no workspace, want every member empty", snapshot.Focus)
	}
	if len(snapshot.Workspaces) != 0 || len(snapshot.Tabs) != 0 ||
		len(snapshot.Panes) != 0 || len(snapshot.Layouts) != 0 {
		t.Errorf("a fresh daemon's snapshot is not empty: %d workspaces, %d tabs, %d panes, %d layouts",
			len(snapshot.Workspaces), len(snapshot.Tabs), len(snapshot.Panes), len(snapshot.Layouts))
	}
}
