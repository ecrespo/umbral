package integration_test

import (
	"bytes"
	"slices"
	"testing"

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

	// The restart.
	restarted := first.restart(t)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
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

	command := []string{"sh", "-c", "echo it ran > /tmp/umbral-must-not-exist"}

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
		if !bytes.Contains(launched.TypeAtPrompt, []byte("echo it ran")) {
			t.Errorf("restored pane %q was given %q to show at its prompt, want the stored "+
				"command: REQ-TERM-011 says leave it *visible*",
				p.ID, launched.TypeAtPrompt)
		}
	}
	if checked == 0 {
		t.Fatal("no restored pane carried a command, so this test asserted nothing")
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
