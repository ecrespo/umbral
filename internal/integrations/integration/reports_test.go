package integration_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/integrations/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestExternalReportDrivesRollup_REQ_INT_002: a report from a stable source becomes the
// pane's state and its source, rolls up to the workspace, and is told to clients as
// `pane.state_changed` and `workspace.updated` — once it is written.
func TestExternalReportDrivesRollup_REQ_INT_002(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newPane(t)
	paneID := tree.RootPane.ID

	if err := h.ReportState(t.Context(), domain.Report{
		PaneID: paneID, Source: "claude-code", Agent: "claude", State: domain.StateBlocked,
		Message: "approve the edit?",
	}); err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	pane := h.pane(t, paneID)
	if pane.AttentionState != wsdomain.AttentionBlocked || pane.StateSource != "claude-code" {
		t.Errorf("the pane is (%s, %q), want (blocked, claude-code)", pane.AttentionState, pane.StateSource)
	}
	workspaces, err := h.tree.ListWorkspaces(t.Context())
	if err != nil || len(workspaces) != 1 || workspaces[0].RollupState != wsdomain.AttentionBlocked {
		t.Errorf("the rollup is %+v (%v), want blocked", workspaces, err)
	}

	events := h.drain()
	want := []ports.PaneStateChanged{{PaneID: paneID, AttentionState: wsdomain.AttentionBlocked, StateSource: "claude-code"}}
	if got := stateChanges(events); !slices.Equal(got, want) {
		t.Errorf("pane.state_changed: %+v, want %+v", got, want)
	}
	if got := rollups(events); !slices.Equal(got, []wsdomain.AttentionState{wsdomain.AttentionBlocked}) {
		t.Errorf("workspace.updated rollups: %v, want [blocked]", got)
	}

	var agent, message string
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT agent, message FROM pane_state_reports WHERE pane_id = ? AND source = 'claude-code'`,
		paneID).Scan(&agent, &message); err != nil || agent != "claude" || message != "approve the edit?" {
		t.Errorf("the stored report is (%q, %q, %v)", agent, message, err)
	}

	// The same state again changes nothing a client sees.
	if err := h.ReportState(t.Context(), domain.Report{
		PaneID: paneID, Source: "claude-code", State: domain.StateBlocked,
	}); err != nil {
		t.Fatalf("ReportState again: %v", err)
	}
	if events := h.drain(); len(events) != 0 {
		t.Errorf("a repeated report published %+v", events)
	}
}

// TestReportReachesAMovedPaneByItsAlias: a hook started before its pane moved still holds the
// old UMBRAL_PANE_ID, and its report reaches the pane under its new id (REQ-WS-007).
func TestReportReachesAMovedPaneByItsAlias(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newPane(t)
	split, _, err := h.tree.SplitPane(t.Context(), wsdomain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: wsdomain.SplitRight,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	moved, err := h.tree.MovePane(t.Context(), wsdomain.MoveParams{
		PaneID: split.ID, Destination: wsdomain.MoveDestination{Type: wsdomain.MoveToNewWorkspace},
	})
	if err != nil {
		t.Fatalf("MovePane: %v", err)
	}
	if err := h.ReportState(t.Context(), domain.Report{
		PaneID: split.ID, Source: "codex", State: domain.StateWorking,
	}); err != nil {
		t.Fatalf("ReportState by the old id: %v", err)
	}
	if pane := h.pane(t, moved.Pane.ID); pane.StateSource != "codex" {
		t.Errorf("the moved pane is (%s, %q), want codex's report", pane.AttentionState, pane.StateSource)
	}
	if err := h.ReleaseState(t.Context(), split.ID, "codex"); err != nil {
		t.Fatalf("ReleaseState by the old id: %v", err)
	}
	if pane := h.pane(t, moved.Pane.ID); pane.StateSource == "codex" {
		t.Errorf("the release by the old id left codex in charge")
	}
}

// TestStaleSeqIgnored_REQ_INT_003: a report whose seq is not past the last accepted one of
// its source is accepted and changes nothing — not the state, not the stored seq, and no
// notification.
func TestStaleSeqIgnored_REQ_INT_003(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	paneID := h.newPane(t).RootPane.ID
	report := func(state domain.State, n *int64) {
		t.Helper()
		if err := h.ReportState(t.Context(), domain.Report{
			PaneID: paneID, Source: "claude-code", State: state, Seq: n,
		}); err != nil {
			t.Fatalf("ReportState(%s, %v): %v", state, n, err)
		}
	}
	expect := func(step string, state wsdomain.AttentionState, storedSeq int64) {
		t.Helper()
		if pane := h.pane(t, paneID); pane.AttentionState != state {
			t.Errorf("%s: the pane is %s, want %s", step, pane.AttentionState, state)
		}
		var got int64
		if err := h.store.DB().QueryRowContext(t.Context(),
			`SELECT seq FROM pane_state_reports WHERE pane_id = ? AND source = 'claude-code'`, paneID).Scan(&got); err != nil || got != storedSeq {
			t.Errorf("%s: the stored seq is %d (%v), want %d", step, got, err, storedSeq)
		}
	}

	report(domain.StateWorking, seq(5))
	expect("the first report", wsdomain.AttentionWorking, 5)
	h.drain()

	report(domain.StateBlocked, seq(5))
	expect("an equal seq", wsdomain.AttentionWorking, 5)
	report(domain.StateDone, seq(4))
	expect("a lower seq", wsdomain.AttentionWorking, 5)
	if events := h.drain(); len(events) != 0 {
		t.Errorf("a stale report published %+v", events)
	}

	report(domain.StateDone, seq(6))
	expect("a later seq", wsdomain.AttentionDone, 6)
	report(domain.StateIdle, nil)
	expect("no seq applies and keeps the last", wsdomain.AttentionIdle, 6)
	report(domain.StateBlocked, seq(6))
	expect("the kept seq still bounds", wsdomain.AttentionIdle, 6)

	// A source whose first report was unnumbered has accepted no number, so its first
	// numbered report applies even at 0.
	if err := h.ReportState(t.Context(), domain.Report{PaneID: paneID, Source: "late-numberer", State: domain.StateWorking}); err != nil {
		t.Fatalf("ReportState(late-numberer): %v", err)
	}
	if err := h.ReportState(t.Context(), domain.Report{PaneID: paneID, Source: "late-numberer", State: domain.StateDone, Seq: seq(0)}); err != nil {
		t.Fatalf("ReportState(late-numberer, 0): %v", err)
	}
	if pane := h.pane(t, paneID); pane.AttentionState != wsdomain.AttentionDone || pane.StateSource != "late-numberer" {
		t.Errorf("a first numbered report at 0 was discarded: (%s, %q)", pane.AttentionState, pane.StateSource)
	}
	h.exec(t, `DELETE FROM pane_state_reports WHERE source = 'late-numberer'`)

	// The seq is per source: another source starts its own count.
	if err := h.ReportState(t.Context(), domain.Report{
		PaneID: paneID, Source: "codex", State: domain.StateBlocked, Seq: seq(1),
	}); err != nil {
		t.Fatalf("ReportState(codex): %v", err)
	}
	if pane := h.pane(t, paneID); pane.AttentionState != wsdomain.AttentionBlocked || pane.StateSource != "codex" {
		t.Errorf("another source's seq 1 was discarded: (%s, %q)", pane.AttentionState, pane.StateSource)
	}
}

// TestReleaseRestoresOwnDetection_REQ_INT_005: once its source releases a pane, the pane's
// state is Umbral's own detection again — here, the block lifecycle's `working` — and clients
// are told so.
func TestReleaseRestoresOwnDetection_REQ_INT_005(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newPane(t)
	paneID := tree.RootPane.ID
	h.exec(t, `INSERT INTO blocks(id, session_id, origin, state, started_at)
	           VALUES ('blk_1', ?, 'user', 'running', 1)`, tree.RootPane.SessionID)
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if pane := h.pane(t, paneID); pane.AttentionState != wsdomain.AttentionWorking || pane.StateSource != wsdomain.SourceShell {
		t.Fatalf("before any report the pane is (%s, %q)", pane.AttentionState, pane.StateSource)
	}

	if err := h.ReportState(t.Context(), domain.Report{
		PaneID: paneID, Source: "claude-code", State: domain.StateBlocked,
	}); err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	// Another source's release, and a release of a pane it never reported for, change nothing.
	if err := h.ReleaseState(t.Context(), paneID, "codex"); err != nil {
		t.Fatalf("ReleaseState(codex): %v", err)
	}
	if pane := h.pane(t, paneID); pane.StateSource != "claude-code" {
		t.Errorf("another source's release displaced claude-code: %q", pane.StateSource)
	}
	h.drain()

	if err := h.ReleaseState(t.Context(), paneID, "claude-code"); err != nil {
		t.Fatalf("ReleaseState: %v", err)
	}
	if pane := h.pane(t, paneID); pane.AttentionState != wsdomain.AttentionWorking || pane.StateSource != wsdomain.SourceShell {
		t.Errorf("after the release the pane is (%s, %q), want (working, umbral:shell)",
			pane.AttentionState, pane.StateSource)
	}
	want := []ports.PaneStateChanged{{PaneID: paneID, AttentionState: wsdomain.AttentionWorking, StateSource: wsdomain.SourceShell}}
	if got := stateChanges(h.drain()); !slices.Equal(got, want) {
		t.Errorf("pane.state_changed: %+v, want %+v", got, want)
	}
	var left int
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM pane_state_reports`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d reports left after the release (%v)", left, err)
	}
	if err := h.ReleaseState(t.Context(), paneID, "claude-code"); err != nil {
		t.Errorf("a second release: %v", err)
	}
}

// TestOwnAgentCannotBeDisplaced_REQ_INT_006: an external source reporting for a pane whose
// live session is a thread of Umbral's own agent gets PERMISSION_DENIED, nothing is written,
// and the pane keeps the agent's state.
func TestOwnAgentCannotBeDisplaced_REQ_INT_006(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newPane(t)
	paneID := tree.RootPane.ID
	h.exec(t, `INSERT INTO threads(id, cwd, attention_state, created_at, updated_at)
	           VALUES ('thr_1', '/tmp', 'working', 1, 1)`)
	h.exec(t, `UPDATE sessions SET owner_thread_id = 'thr_1' WHERE id = ?`, tree.RootPane.SessionID)
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h.drain()

	err := h.ReportState(t.Context(), domain.Report{
		PaneID: paneID, Source: "claude-code", State: domain.StateBlocked,
	})
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("ReportState on the agent's pane: %v, want permission denied", err)
	}
	pane := h.pane(t, paneID)
	if pane.AttentionState != wsdomain.AttentionWorking || pane.StateSource != wsdomain.SourceAgent || pane.ThreadID != "thr_1" {
		t.Errorf("the pane is (%s, %q, %q), want the agent's", pane.AttentionState, pane.StateSource, pane.ThreadID)
	}
	var stored int
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT count(*) FROM pane_state_reports`).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("a refused report was stored (%d, %v)", stored, err)
	}
	if events := h.drain(); len(events) != 0 {
		t.Errorf("a refused report published %+v", events)
	}
}

// TestReportsForMissingPanes: an unknown or closed pane is NOT_FOUND, and a malformed report
// is a validation error before anything is read.
func TestReportsForMissingPanes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newPane(t)
	split, _, err := h.tree.SplitPane(t.Context(), wsdomain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: wsdomain.SplitDown,
	})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if err := h.tree.ClosePane(t.Context(), split.ID); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	for _, id := range []string{"w9:p9", split.ID} {
		err := h.ReportState(t.Context(), domain.Report{PaneID: id, Source: "x", State: domain.StateIdle})
		if !errors.Is(err, wsdomain.ErrNotFound) {
			t.Errorf("ReportState(%s): %v, want not found", id, err)
		}
		if err := h.ReleaseState(t.Context(), id, "x"); !errors.Is(err, wsdomain.ErrNotFound) {
			t.Errorf("ReleaseState(%s): %v, want not found", id, err)
		}
	}
	err = h.ReportState(t.Context(), domain.Report{PaneID: tree.RootPane.ID, Source: "umbral:agent", State: domain.StateIdle})
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a report as umbral:agent: %v, want a validation error", err)
	}
	if err := h.ReleaseState(t.Context(), tree.RootPane.ID, "bad source"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a release with a bad source: %v, want a validation error", err)
	}
}

// TestRefreshFollowsOwnDetection_DD_012: a change no report made — a command starting —
// reaches clients as `pane.state_changed` from `umbral:shell` once the daemon refreshes.
func TestRefreshFollowsOwnDetection_DD_012(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tree := h.newPane(t)
	h.exec(t, `INSERT INTO blocks(id, session_id, origin, state, started_at)
	           VALUES ('blk_1', ?, 'user', 'running', 1)`, tree.RootPane.SessionID)
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := []ports.PaneStateChanged{{PaneID: tree.RootPane.ID, AttentionState: wsdomain.AttentionWorking, StateSource: wsdomain.SourceShell}}
	if got := stateChanges(h.drain()); !slices.Equal(got, want) {
		t.Errorf("pane.state_changed: %+v, want %+v", got, want)
	}
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh again: %v", err)
	}
	if events := h.drain(); len(events) != 0 {
		t.Errorf("a refresh with nothing changed published %+v", events)
	}
}

// TestTheLatestReportWins_REQ_INT_002: with two sources reporting for one pane, the pane
// follows whichever reported last, even within one millisecond, and a release hands the pane
// to the other one.
func TestTheLatestReportWins_REQ_INT_002(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	paneID := h.newPane(t).RootPane.ID
	report := func(source string, state domain.State) {
		t.Helper()
		if err := h.ReportState(t.Context(), domain.Report{PaneID: paneID, Source: source, State: state}); err != nil {
			t.Fatalf("ReportState(%s): %v", source, err)
		}
	}
	expect := func(step, source string) {
		t.Helper()
		if pane := h.pane(t, paneID); pane.StateSource != source {
			t.Errorf("%s: the pane follows %q, want %q", step, pane.StateSource, source)
		}
	}
	report("alpha", domain.StateBlocked)
	report("beta", domain.StateDone)
	expect("the second source", "beta")
	report("alpha", domain.StateWorking)
	expect("the first source again", "alpha")
	if err := h.ReleaseState(t.Context(), paneID, "alpha"); err != nil {
		t.Fatalf("ReleaseState: %v", err)
	}
	expect("after the release", "beta")
}
