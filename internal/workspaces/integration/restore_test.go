package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestRestoreRebuildsStructure_REQ_TERM_009 is the criterion: a daemon that starts after a
// shutdown comes back with the workspaces, tabs and panes it had, each with a fresh shell.
//
// The second service is the point. It is built over the *same database* with its own fake
// terminals, which is what a restart is: the rows survive, the processes do not. A test that
// called Restore on the service that created the tree would prove nothing, because that
// service already holds the answer in memory.
func TestRestoreRebuildsStructure_REQ_TERM_009(t *testing.T) {
	t.Parallel()

	first := newHarness(t)
	tree := first.newWorkspace(t, "before")
	if _, _, err := first.SplitPane(t.Context(), domain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: domain.SplitRight, Ratio: 0.5, CWD: t.TempDir(),
	}); err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if _, _, err := first.CreateTab(t.Context(), tree.Workspace.ID, "second", true); err != nil {
		t.Fatalf("CreateTab: %v", err)
	}

	before, err := first.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot before: %v", err)
	}
	firstSessions := first.terminals.createdIDs()

	// The restart, in the order `cmd/umbrald` does it: the store's recovery runs before
	// the tree is rebuilt. That order is REQ-TERM-009's third clause — "mark the sessions
	// of the previous run as `exited`" — and it is asserted below rather than assumed,
	// because a restore that ran first would hand every pane a session the daemon still
	// believed was alive.
	if _, err := first.store.Recover(t.Context(), time.Now()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	restarted := first.restart(t)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// No session of the previous run is still `alive`. `internal/store` proves the
	// recovery in isolation; what this adds is that it has happened by the time the tree
	// is back, which is the clause's actual claim.
	for _, id := range firstSessions {
		var state string
		if err := first.store.DB().QueryRowContext(t.Context(),
			`SELECT state FROM sessions WHERE id = ?`, id).Scan(&state); err != nil {
			t.Fatalf("read the state of %s: %v", id, err)
		}
		if state != "exited" {
			t.Errorf("session %q of the previous run is %q after the restart, want "+
				"\"exited\" (REQ-TERM-009)", id, state)
		}
	}

	after, err := restarted.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot after: %v", err)
	}

	if len(after.Workspaces) != len(before.Workspaces) ||
		len(after.Tabs) != len(before.Tabs) || len(after.Panes) != len(before.Panes) {
		t.Fatalf("restored %d workspaces, %d tabs, %d panes; had %d, %d, %d",
			len(after.Workspaces), len(after.Tabs), len(after.Panes),
			len(before.Workspaces), len(before.Tabs), len(before.Panes))
	}

	// Every pane has a terminal again, and it is a new one: the previous run's sessions
	// died with it, and a restored pane still pointing at one would name a dead process.
	for _, pane := range after.Panes {
		if pane.SessionID == "" {
			t.Errorf("restored pane %q has no terminal", pane.ID)
		}
		if slices.Contains(firstSessions, pane.SessionID) {
			t.Errorf("restored pane %q kept the previous run's session %q",
				pane.ID, pane.SessionID)
		}
	}

	// Labels and working directories come back, which is what REQ-TERM-009 names.
	labelsBefore := labels(before.Tabs)
	labelsAfter := labels(after.Tabs)
	if !slices.Equal(labelsBefore, labelsAfter) {
		t.Errorf("tab labels after the restart are %v, want %v", labelsAfter, labelsBefore)
	}
}

// TestRestoreKeepsFocus_REQ_TERM_009 pins the half that used to live only in memory.
//
// Before T-F0-18 focus had no column at all, so a restarted daemon answered `layout.export`
// with no `tab_id` as "nothing is focused" and a client opened on whichever workspace sorted
// first. The cost was small and the fix belonged here, with the rest of the restore.
func TestRestoreKeepsFocus_REQ_TERM_009(t *testing.T) {
	t.Parallel()

	first := newHarness(t)
	_ = first.newWorkspace(t, "one")
	two := first.newWorkspace(t, "two")

	// Focus the older workspace's second tab, so the answer is not simply "the last one
	// created" — which a restore that guessed would also produce.
	wanted, _, err := first.CreateTab(t.Context(), two.Workspace.ID, "wanted", true)
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	wsBefore, tabBefore := first.Focused()
	if wsBefore != two.Workspace.ID || tabBefore != wanted.ID {
		t.Fatalf("focus before the restart is {%q, %q}, want {%q, %q}",
			wsBefore, tabBefore, two.Workspace.ID, wanted.ID)
	}

	restarted := first.restart(t)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	wsAfter, tabAfter := restarted.Focused()
	if wsAfter != wsBefore || tabAfter != tabBefore {
		t.Errorf("focus after the restart is {%q, %q}, want {%q, %q}",
			wsAfter, tabAfter, wsBefore, tabBefore)
	}
}

// TestRestoreNeverRunsStoredCommand_REQ_TERM_011 is the requirement that made this task need
// a Delta.
//
// Data Model §6 step 5 said a restored pane "launches a fresh shell, **or its `command_json`
// when it has one**", and REQ-TERM-011 says the opposite in as many words: "leave it visible
// in the pane without running it … so that a restart never re-executes commands on its own".
// The danger is concrete — a pane whose command was `terraform apply` or `make deploy` would
// re-run unattended on every start, possibly after a crash that command caused.
//
// So this asserts three things, and the middle one is the requirement: the terminal is a
// shell, the command was **not** given to it to run, and the command text is handed over to
// be shown at the prompt.
func TestRestoreNeverRunsStoredCommand_REQ_TERM_011(t *testing.T) {
	t.Parallel()

	// The sentinel is inside the test's own directory, so its absence is checked rather
	// than asserted about a path shared with every other run on the machine. These
	// terminals are fakes and cannot run anything, so the absence proves little here; it
	// is `TestAPendingCommandIsShownAndNotRun_REQ_TERM_011` in the sessions integration
	// package, against a real PTY and a real shell, that proves the command does not run.
	sentinel := filepath.Join(t.TempDir(), "it-ran")
	command := []string{"sh", "-c", "echo it ran > " + sentinel}
	// The exact text a restored pane must be given to show. Asserting the whole line and
	// not a substring is deliberate: `bytes.Contains` is satisfied by a trailing newline,
	// and a trailing newline is precisely what makes the shell run the command.
	wantLine := "sh -c 'echo it ran > " + sentinel + "'"

	first := newHarness(t)
	tree := first.newWorkspace(t, "commands")
	pane, _, err := first.SplitPane(t.Context(), domain.SplitParams{
		PaneID: tree.RootPane.ID, Direction: domain.SplitRight, Ratio: 0.5,
		CWD: t.TempDir(), Command: command,
	})
	if err != nil {
		t.Fatalf("SplitPane with a command: %v", err)
	}
	// `pane.split` is the one path that *does* run it: a client asking for a command now
	// is asking for it now. That contrast is what makes the restore behaviour meaningful.
	if launched, ok := first.terminals.launchedWith(pane.SessionID); !ok {
		t.Fatal("pane.split started no terminal")
	} else if !slices.Equal(launched.Command, command) {
		t.Errorf("pane.split launched %v, want %v — REQ-TERM-011 governs restore and "+
			"layout.apply, not an explicit split", launched.Command, command)
	}
	if pane.CommandPending {
		t.Error("a pane.split command is reported pending; it was run")
	}

	restarted := first.restart(t)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	after, err := restarted.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	var checked int
	for _, p := range after.Panes {
		if len(p.Command) == 0 {
			continue
		}
		checked++
		if !slices.Equal(p.Command, command) {
			t.Errorf("restored pane %q records command %v, want %v", p.ID, p.Command, command)
		}
		if !p.CommandPending {
			t.Errorf("restored pane %q does not report its command as pending", p.ID)
		}

		launched, ok := restarted.terminals.launchedWith(p.SessionID)
		if !ok {
			t.Errorf("restored pane %q started no terminal", p.ID)
			continue
		}
		if len(launched.Command) != 0 {
			t.Errorf("the restart launched %v for pane %q; REQ-TERM-011 forbids re-running "+
				"a stored command", launched.Command, p.ID)
		}
		if !launched.ShellIntegration {
			t.Errorf("restored pane %q runs a shell and must have shell integration", p.ID)
		}
		if string(launched.TypeAtPrompt) != wantLine {
			t.Errorf("restored pane %q was given %q to show at its prompt, want exactly "+
				"%q: REQ-TERM-011 says leave it *visible*, and any trailing line ending "+
				"would make the shell run it",
				p.ID, launched.TypeAtPrompt, wantLine)
		}
		if bytes.ContainsAny(launched.TypeAtPrompt, "\n\r") {
			t.Errorf("restored pane %q was given text containing a line ending (%q); the "+
				"shell would submit it", p.ID, launched.TypeAtPrompt)
		}
	}
	if checked == 0 {
		t.Fatal("no restored pane carried a command, so this test asserted nothing")
	}

	// Nothing ran. With fake terminals this cannot fail, and it is kept as the statement
	// of what the whole test is about rather than as evidence — the evidence is in the
	// sessions integration package, where a real shell is asked the same question.
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("the stored command ran: %s exists", sentinel)
	}
}

func labels(tabs []domain.Tab) []string {
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		out = append(out, tab.Label)
	}
	slices.Sort(out)
	return out
}

// TestPaneHistoryReplaysOnlyWhenEnabled_REQ_TERM_010 covers both halves of the setting.
//
// The default is the one that matters: "the setting SHALL be disabled by default because pane
// output can contain secrets". A daemon that captured screens anyway would be writing exactly
// what the requirement is cautious about into SQLite, and nobody would be told. So the first
// half asserts that nothing is stored, and the second that opting in works — a test that only
// checked the second would pass on a daemon that ignored the setting entirely.
func TestPaneHistoryReplaysOnlyWhenEnabled_REQ_TERM_010(t *testing.T) {
	t.Parallel()

	const screen = "\x1b[32mwhat the pane showed before\x1b[0m\r\n"

	t.Run("off by default", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithHistory(t, false, screen)
		tree := h.newWorkspace(t, "no history")
		h.CaptureScreens(t.Context())

		stored, err := h.screens.LoadScreen(t.Context(), tree.RootPane.ID)
		if err != nil {
			t.Fatalf("read the stored screen: %v", err)
		}
		if len(stored) != 0 {
			t.Errorf("a screen was captured with the setting off: %q", stored)
		}

		restarted := h.restartWithHistory(t, false, screen)
		if err := restarted.Restore(t.Context()); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		for _, launched := range restarted.terminals.allLaunched() {
			if len(launched.ReplayScreen) != 0 {
				t.Errorf("a screen was replayed with the setting off: %q", launched.ReplayScreen)
			}
		}
	})

	t.Run("on when asked", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithHistory(t, true, screen)
		tree := h.newWorkspace(t, "history")
		h.CaptureScreens(t.Context())

		stored, err := h.screens.LoadScreen(t.Context(), tree.RootPane.ID)
		if err != nil {
			t.Fatalf("read the stored screen: %v", err)
		}
		if !bytes.Equal(stored, []byte(screen)) {
			t.Fatalf("stored screen is %q, want %q", stored, screen)
		}

		restarted := h.restartWithHistory(t, true, screen)
		if err := restarted.Restore(t.Context()); err != nil {
			t.Fatalf("Restore: %v", err)
		}

		var replayed int
		for _, launched := range restarted.terminals.allLaunched() {
			if bytes.Equal(launched.ReplayScreen, []byte(screen)) {
				replayed++
			}
		}
		if replayed == 0 {
			t.Error("the stored screen was not replayed into any restored pane")
		}
	})
}

// TestTurningPaneHistoryOffForgetsWhatWasStored_REQ_TERM_010 is the privacy half.
//
// Data Model §2.4d: "Turning the setting off deletes the table's contents at the next start."
// That is a promise, not housekeeping. A user who turns pane history off is asking for what
// was captured to stop existing — screens the requirement itself says "can contain secrets" —
// and keeping the rows in case they change their mind answers a question they did not ask.
//
// It is also what makes the replay guard meaningful. With the setting off nothing new is
// captured, so a replay could only ever come from rows an earlier run left; deleting them is
// what closes that path rather than merely declining to read it.
func TestTurningPaneHistoryOffForgetsWhatWasStored_REQ_TERM_010(t *testing.T) {
	t.Parallel()

	const screen = "a screen captured while the user was opted in\r\n"

	h := newHarnessWithHistory(t, true, screen)
	tree := h.newWorkspace(t, "opted in")
	h.CaptureScreens(t.Context())

	stored, err := h.screens.LoadScreen(t.Context(), tree.RootPane.ID)
	if err != nil {
		t.Fatalf("read the stored screen: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("nothing was captured while the setting was on, so this test asserts nothing")
	}

	// The next start, with the setting off.
	off := h.restartWithHistory(t, false, screen)
	if err := off.PrepareHistory(t.Context()); err != nil {
		t.Fatalf("PrepareHistory: %v", err)
	}

	after, err := off.screens.LoadScreen(t.Context(), tree.RootPane.ID)
	if err != nil {
		t.Fatalf("read the screen after turning the setting off: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("a screen captured earlier survived the setting being turned off: %q", after)
	}

	// And a restart with it off replays nothing, which is now true because there is
	// nothing left rather than because a guard declined to look.
	if err := off.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, launched := range off.terminals.allLaunched() {
		if len(launched.ReplayScreen) != 0 {
			t.Errorf("a screen was replayed after the setting was turned off: %q",
				launched.ReplayScreen)
		}
	}
}

// TestRestoreDoesNotAdoptFocusOnAClosedTab_REQ_TERM_009 closes the gap between the two ways a
// tab stops existing.
//
// `workspaces.focused_tab_id` is declared ON DELETE SET NULL, which reads like it handles
// this — and does not, because closing a tab is an UPDATE and the row is never deleted. So
// the id outlives its tab, and a restart that trusted the column would come up focused on a
// tab that is gone. The symptom is not a crash: `layout.export` with no `tab_id` resolves the
// focused tab and answers "tab w1:t1 does not exist", which is exactly the failure the
// default was added to prevent.
//
// The workspace still comes back focused. Losing which tab was in use is a small cost; losing
// the workspace as well would send a client back to whichever one sorts first.
func TestRestoreDoesNotAdoptFocusOnAClosedTab_REQ_TERM_009(t *testing.T) {
	t.Parallel()

	first := newHarness(t)
	tree := first.newWorkspace(t, "w")

	doomed, _, err := first.CreateTab(t.Context(), tree.Workspace.ID, "doomed", true)
	if err != nil {
		t.Fatalf("CreateTab: %v", err)
	}
	if _, tab := first.Focused(); tab != doomed.ID {
		t.Fatalf("focus is on %q, want the tab just created (%q)", tab, doomed.ID)
	}
	if err := first.CloseTab(t.Context(), doomed.ID); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	restarted := first.restart(t)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	ws, tab := restarted.Focused()
	if ws != tree.Workspace.ID {
		t.Errorf("focused workspace after the restart is %q, want %q", ws, tree.Workspace.ID)
	}
	if tab == doomed.ID {
		t.Fatalf("the restart adopted focus on the closed tab %q; layout.export with no "+
			"tab_id would answer that it does not exist", tab)
	}

	// And the default actually resolves, which is the outcome this is protecting.
	if _, err := restarted.ExportLayout(t.Context(), ""); err != nil {
		t.Errorf("layout.export with no tab_id failed after the restart: %v", err)
	}
}

// TestClosingAPaneForgetsItsScreen_REQ_TERM_010 is the retention half of the requirement.
//
// Data Model §4 gives `pane_history` a retention of "until the pane closes". Nothing was
// enforcing it. The table is declared `ON DELETE CASCADE`, which reads like it handles this,
// but a pane is closed with `UPDATE panes SET closed_at = ?` and is never deleted — so the
// cascade never fired, there was no sweeper, and the captured screen of every pane the user
// ever closed stayed in the database for the life of the installation.
//
// That is a privacy defect rather than an untidiness. REQ-TERM-010 disables the setting by
// default "because pane output can contain secrets", and a user who opted in accepted storage
// for the panes they are using, not an archive of every pane they have ever closed.
//
// All three closing paths, because they are three different statements and only one of them
// would have been noticed.
func TestClosingAPaneForgetsItsScreen_REQ_TERM_010(t *testing.T) {
	t.Parallel()

	const screen = "a captured screen that must not outlive its pane\r\n"

	t.Run("pane.close", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithHistory(t, true, screen)
		tree := h.newWorkspace(t, "closing a pane")
		// A tab keeps at least one pane, so the split gives us one that can be closed
		// while the tab stays open — which is the path that has to forget on its own.
		extra, _, err := h.SplitPane(t.Context(), domain.SplitParams{
			PaneID: tree.RootPane.ID, Direction: domain.SplitRight, Ratio: 0.5,
			CWD: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("SplitPane: %v", err)
		}
		h.CaptureScreens(t.Context())
		requireStored(t, h, extra.ID)

		if err := h.ClosePane(t.Context(), extra.ID); err != nil {
			t.Fatalf("ClosePane: %v", err)
		}
		requireForgotten(t, h, extra.ID)

		// The pane that stayed open keeps its screen: the delete is targeted, not a
		// convenient way of emptying the table.
		requireStored(t, h, tree.RootPane.ID)
	})

	t.Run("tab.close", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithHistory(t, true, screen)
		tree := h.newWorkspace(t, "closing a tab")
		tab, tabPane, err := h.CreateTab(t.Context(), tree.Workspace.ID, "doomed", false)
		if err != nil {
			t.Fatalf("CreateTab: %v", err)
		}
		h.CaptureScreens(t.Context())
		requireStored(t, h, tabPane.ID)

		if err := h.CloseTab(t.Context(), tab.ID); err != nil {
			t.Fatalf("CloseTab: %v", err)
		}
		requireForgotten(t, h, tabPane.ID)
		requireStored(t, h, tree.RootPane.ID)
	})

	t.Run("workspace.close", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithHistory(t, true, screen)
		kept := h.newWorkspace(t, "kept")
		doomed := h.newWorkspace(t, "doomed")
		h.CaptureScreens(t.Context())
		requireStored(t, h, doomed.RootPane.ID)

		if err := h.CloseWorkspace(t.Context(), doomed.Workspace.ID, true); err != nil {
			t.Fatalf("CloseWorkspace: %v", err)
		}
		requireForgotten(t, h, doomed.RootPane.ID)
		requireStored(t, h, kept.RootPane.ID)
	})
}

func requireStored(t *testing.T, h *harness, paneID string) {
	t.Helper()
	stored, err := h.screens.LoadScreen(t.Context(), paneID)
	if err != nil {
		t.Fatalf("read the stored screen of %s: %v", paneID, err)
	}
	if len(stored) == 0 {
		t.Fatalf("no screen is stored for pane %s, so the test would assert nothing", paneID)
	}
}

func requireForgotten(t *testing.T, h *harness, paneID string) {
	t.Helper()
	stored, err := h.screens.LoadScreen(t.Context(), paneID)
	if err != nil {
		t.Fatalf("read the stored screen of %s: %v", paneID, err)
	}
	if len(stored) != 0 {
		t.Errorf("the screen of the closed pane %s survived it: %q. Data Model §4 keeps "+
			"pane_history only \"until the pane closes\"", paneID, stored)
	}
}
