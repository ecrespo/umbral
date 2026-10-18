package integration_test

import (
	"testing"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestPaneStateFollowsItsAuthority_DD_012 drives a pane through every authority DD-012
// names, by writing the rows each one owns, and reads the pane and its workspace back. A pane
// reports exactly one source at a time, and the workspace's rollup is that state.
func TestPaneStateFollowsItsAuthority_DD_012(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newWorkspace(t, "attn")
	paneID, sessionID := tree.RootPane.ID, tree.RootPane.SessionID
	db := h.store.DB()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	expect := func(step string, state domain.AttentionState, source, threadID string) {
		t.Helper()
		pane, err := h.GetPane(t.Context(), paneID)
		if err != nil {
			t.Fatalf("%s: GetPane: %v", step, err)
		}
		if pane.AttentionState != state || pane.StateSource != source || pane.ThreadID != threadID {
			t.Errorf("%s: pane is (%s, %q, thread %q), want (%s, %q, thread %q)", step,
				pane.AttentionState, pane.StateSource, pane.ThreadID, state, source, threadID)
		}
		listed, err := h.ListPanes(t.Context(), pane.TabID)
		if err != nil || len(listed) != 1 || listed[0].AttentionState != state {
			t.Errorf("%s: ListPanes gives %+v (%v), want one pane in %s", step, listed, err, state)
		}
		workspaces, err := h.ListWorkspaces(t.Context())
		if err != nil || len(workspaces) != 1 || workspaces[0].RollupState != state {
			t.Errorf("%s: the rollup is %+v (%v), want %s", step, workspaces, err, state)
		}
	}

	expect("integration still pending", domain.AttentionUnknown, "", "")

	exec(`UPDATE sessions SET integration = 'osc133' WHERE id = ?`, sessionID)
	expect("a shell at its first prompt", domain.AttentionIdle, domain.SourceShell, "")

	exec(`INSERT INTO blocks(id, session_id, origin, state, started_at)
	      VALUES ('blk_1', ?, 'user', 'finished', 1)`, sessionID)
	exec(`INSERT INTO blocks(id, session_id, origin, state, started_at)
	      VALUES ('blk_2', ?, 'user', 'running', 2)`, sessionID)
	expect("its latest command running", domain.AttentionWorking, domain.SourceShell, "")

	exec(`INSERT INTO pane_state_reports(pane_id, source, state, updated_at)
	      VALUES (?, 'claude-hook', 'done', 10)`, paneID)
	exec(`INSERT INTO pane_state_reports(pane_id, source, state, updated_at)
	      VALUES (?, 'codex-hook', 'blocked', 20)`, paneID)
	expect("the latest of two reports", domain.AttentionBlocked, "codex-hook", "")

	exec(`DELETE FROM pane_state_reports WHERE source = 'codex-hook'`)
	expect("the other report once one is released", domain.AttentionDone, "claude-hook", "")

	exec(`INSERT INTO threads(id, cwd, attention_state, created_at, updated_at)
	      VALUES ('thr_1', '/tmp', 'working', 1, 1)`)
	exec(`UPDATE sessions SET owner_thread_id = 'thr_1' WHERE id = ?`, sessionID)
	expect("the own agent over a report", domain.AttentionWorking, domain.SourceAgent, "thr_1")

	exec(`UPDATE sessions SET owner_thread_id = NULL, state = 'exited' WHERE id = ?`, sessionID)
	exec(`DELETE FROM pane_state_reports`)
	expect("a session that exited", domain.AttentionUnknown, "", "")
}

// TestPaneStateReportsFollowAMove: a report belongs to the pane, not to its identifier, so a
// pane moved to another workspace keeps it under its new id (REQ-WS-007).
func TestPaneStateReportsFollowAMove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newWorkspace(t, "src")
	pane, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: domain.SplitRight,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if _, err := h.store.DB().ExecContext(t.Context(), `
		INSERT INTO pane_state_reports(pane_id, source, state, seq, updated_at)
		VALUES (?, 'claude-hook', 'blocked', 7, 10)`, pane.ID); err != nil {
		t.Fatalf("insert report: %v", err)
	}

	moved, err := h.MovePane(t.Context(), domain.MoveParams{
		PaneID: pane.ID, Destination: domain.MoveDestination{Type: domain.MoveToNewWorkspace},
	})
	if err != nil {
		t.Fatalf("MovePane: %v", err)
	}
	if moved.Pane.AttentionState != domain.AttentionBlocked || moved.Pane.StateSource != "claude-hook" {
		t.Errorf("the moved pane is (%s, %q), want (blocked, claude-hook)",
			moved.Pane.AttentionState, moved.Pane.StateSource)
	}
	var seq int64
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT seq FROM pane_state_reports WHERE pane_id = ?`, moved.Pane.ID).Scan(&seq); err != nil || seq != 7 {
		t.Errorf("the report under %s has seq %d (%v), want 7", moved.Pane.ID, seq, err)
	}
}

// TestEveryPaneIsToldWhereItIs_REQ_INT_001: the workspaces module hands every session it
// starts the location of its pane — a new workspace's, a split's and a restored one's — so
// the sessions module can inject UMBRAL_WORKSPACE_ID, UMBRAL_TAB_ID and UMBRAL_PANE_ID.
func TestEveryPaneIsToldWhereItIs_REQ_INT_001(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newWorkspace(t, "loc")
	split, _, err := h.SplitPane(t.Context(), domain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: domain.SplitDown, Command: []string{"top"},
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	for _, pane := range []domain.Pane{tree.RootPane, split} {
		params, ok := h.terminals.launchedWith(pane.SessionID)
		if !ok {
			t.Fatalf("pane %s: no session launched", pane.ID)
		}
		if params.Pane.WorkspaceID != pane.WorkspaceID || params.Pane.TabID != pane.TabID ||
			params.Pane.PaneID != pane.ID {
			t.Errorf("pane %s was launched at %+v", pane.ID, params.Pane)
		}
	}

	restarted := h.restart(t)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, params := range restarted.terminals.allLaunched() {
		if params.Pane.PaneID != tree.RootPane.ID && params.Pane.PaneID != split.ID {
			t.Errorf("a restored pane was launched at %+v", params.Pane)
		}
		if params.Pane.WorkspaceID != tree.Workspace.ID || params.Pane.TabID != tree.Tab.ID {
			t.Errorf("a restored pane was launched at %+v", params.Pane)
		}
	}
	if n := len(restarted.terminals.allLaunched()); n != 2 {
		t.Errorf("the restart launched %d sessions, want 2", n)
	}
}
