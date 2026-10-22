package integration_test

import (
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestAPurgedWorkspaceNumberNeverReissuesAnAlias_REQ_WS_007: the retention job (Data Model §4,
// T-F1-22) deletes a workspace closed for 30 days, and its number can then be given to a new
// workspace (REQ-WS-002 makes it unique while the object exists). A pane that moved out of the
// purged workspace keeps its old identifier as an alias for the life of its terminal
// (REQ-WS-007), so the new workspace must never hand that identifier to another pane.
func TestAPurgedWorkspaceNumberNeverReissuesAnAlias_REQ_WS_007(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	destination := h.newWorkspace(t, "destination")
	source := h.newWorkspace(t, "source")
	pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{PaneID: source.RootPane.ID, Direction: domain.SplitRight})
	if err != nil {
		t.Fatal(err)
	}
	alias := pane.ID
	moved, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID: alias, Destination: domain.MoveDestination{Type: domain.MoveToTab, TabID: destination.Tab.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.CloseWorkspace(t.Context(), source.Workspace.ID, true); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	for _, q := range []string{`UPDATE workspaces SET closed_at = ? WHERE closed_at IS NOT NULL`, `UPDATE tabs SET closed_at = ? WHERE closed_at IS NOT NULL`, `UPDATE panes SET closed_at = ? WHERE closed_at IS NOT NULL`} {
		if _, err := h.store.DB().ExecContext(t.Context(), q, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.store.ApplyRetention(t.Context(), time.Now(), store.DefaultRetention()); err != nil {
		t.Fatal(err)
	}

	again := h.newWorkspace(t, "again")
	if again.Workspace.ID != source.Workspace.ID {
		t.Fatalf("the new workspace is %s; the test needs the purged number %s back", again.Workspace.ID, source.Workspace.ID)
	}
	ids := make([]string, 0, 4)
	ids = append(ids, again.RootPane.ID)
	for range 3 {
		p, _, err := h.SplitPane(t.Context(), domain.SplitParams{PaneID: again.RootPane.ID, Direction: domain.SplitDown})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	for _, id := range ids {
		if id == alias {
			t.Fatalf("a new pane got %s, still the alias of the moved pane %s", alias, moved.Pane.ID)
		}
	}
	byAlias, err := h.GetPane(t.Context(), alias)
	if err != nil || byAlias.ID != moved.Pane.ID {
		t.Fatalf("GetPane(%s) after the purge = %+v, %v; want the moved pane %s", alias, byAlias.ID, err, moved.Pane.ID)
	}
}
