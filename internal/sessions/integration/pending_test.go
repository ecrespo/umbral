package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// The delivery side of REQ-TERM-011, against a real PTY and a real shell.
//
// Everything above this point is fakes: the workspaces tests prove the module hands a line to
// the sessions port, which is a different claim from "the line was shown and nothing ran".
// The whole of `internal/sessions/pending.go` — arming, the prompt marker, the grace timer,
// the single-delivery guarantee — had no test at all, so an early return anywhere in it left
// the pane blank and every gate green.
//
// The requirement has two halves and they pull in opposite directions, which is why both are
// asserted here: the command must be *visible* (so it cannot simply be dropped) and it must
// *not run* (so it cannot simply be typed with a newline). A test that checked either one
// alone would be satisfied by the failure of the other.

// TestAPendingCommandIsShownAndNotRun_REQ_TERM_011 is the criterion, end to end.
func TestAPendingCommandIsShownAndNotRun_REQ_TERM_011(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// The sentinel is what makes "did not run" observable. If the shell ever executes the
	// line, this file appears; nothing else in the test can create it.
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "it-ran")
	const marker = "umbral-pending-4c71"
	line := "echo " + marker + " > " + sentinel

	session := h.create(t, domain.CreateParams{
		CWD: dir, ShellIntegration: true, TypeAtPrompt: []byte(line),
	})

	// Visible: the text reaches the screen, because the shell echoes what was typed into
	// its line editor. This is REQ-TERM-011's "leave it visible in the pane".
	if !eventuallyContains(t, h, session.ID, marker) {
		snapshot, _ := h.Snapshot(t.Context(), session.ID)
		t.Fatalf("the pending command never appeared on the screen; REQ-TERM-011 says to "+
			"leave it visible. Screen was:\n%s", snapshot.Data)
	}

	// Not run. The command was on screen above, so the shell has had it in its line editor;
	// giving it a further second is generous for a redirection into a local file.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sentinel); err == nil {
			t.Fatalf("the pending command ran on its own: %s exists. REQ-TERM-011 says a "+
				"restart never re-executes commands.", sentinel)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// And it runs when the user confirms, which is the rest of the same sentence: "SHALL
	// run it only after the user confirms". Without this the requirement would be met by a
	// daemon that showed the text and made it impossible to use.
	if err := h.Input(t.Context(), session.ID, []byte("\r"), domain.InputOwnerHuman); err != nil {
		t.Fatalf("Input: %v", err)
	}
	if !eventuallyExists(t, sentinel) {
		t.Errorf("the command did not run after the user pressed Enter; REQ-TERM-011 says "+
			"it SHALL run once confirmed. %s was never created", sentinel)
	}
}

// TestPendingTextIsDeliveredWithoutShellIntegration_REQ_TERM_011 covers the grace path.
//
// A shell with no OSC 133 integration never reports a prompt, so the prompt-marker route
// cannot fire and the two-second timer is the only thing that delivers. Without it a pane
// launched from a layout on such a shell would sit empty with no sign a command was waiting —
// the requirement's "leave it visible" failing silently while its "without running it" held.
func TestPendingTextIsDeliveredWithoutShellIntegration_REQ_TERM_011(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	dir := t.TempDir()
	sentinel := filepath.Join(dir, "it-ran")
	const marker = "umbral-grace-9b30"

	session := h.create(t, domain.CreateParams{
		CWD: dir, ShellIntegration: false,
		TypeAtPrompt: []byte("echo " + marker + " > " + sentinel),
	})

	if !eventuallyContains(t, h, session.ID, marker) {
		snapshot, _ := h.Snapshot(t.Context(), session.ID)
		t.Fatalf("the pending command never appeared on a session with no shell "+
			"integration; the grace timer is what has to deliver it. Screen was:\n%s",
			snapshot.Data)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("the pending command ran: %s exists", sentinel)
	}
}

// TestPendingTextIsDeliveredOnce_REQ_TERM_011 pins the race the two delivery routes create.
//
// The prompt marker and the grace timer both fire for a session with shell integration: the
// marker first, the timer two seconds later. If the buffer were not cleared under the lock
// the user would find the command typed twice, and pressing Enter would then run it twice —
// for `make deploy` or `terraform apply`, the second run is a real consequence.
func TestPendingTextIsDeliveredOnce_REQ_TERM_011(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	const marker = "umbral-once-2d54"
	session := h.create(t, domain.CreateParams{
		CWD: t.TempDir(), ShellIntegration: true,
		TypeAtPrompt: []byte("echo " + marker),
	})

	if !eventuallyContains(t, h, session.ID, marker) {
		t.Fatal("the pending command never appeared on the screen")
	}

	// Past the grace period, so both routes have had their chance.
	time.Sleep(3 * time.Second)

	snapshot, err := h.Snapshot(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// The screen shows the shell's echo of what was typed. Two deliveries put the text on
	// the line twice, so the marker appears more often than the one echo accounts for.
	if n := strings.Count(string(snapshot.Data), marker); n > 1 {
		t.Errorf("the pending command appears %d times on the screen, want 1: it was "+
			"delivered by both the prompt marker and the grace timer.\nScreen was:\n%s",
			n, snapshot.Data)
	}
}

// eventuallyExists polls for a file, which is how "the command ran" is observed without
// guessing how long a shell takes to fork, redirect and exit.
func eventuallyExists(t *testing.T, path string) bool {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
