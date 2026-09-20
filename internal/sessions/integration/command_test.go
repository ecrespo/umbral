package integration_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// These cover the launch path T-F0-15 added, which until now was exercised only through a
// fake: the workspaces tests prove the module hands an argv to the port, not that the daemon
// runs it. A command that is stored and never executed is the failure REQ-WS-005 is about,
// and it looks identical from above.

// TestSessionRunsACommandInsteadOfAShell_REQ_WS_005 is the launch path end to end: a real
// PTY, a real process, and its output read back off the emulator.
func TestSessionRunsACommandInsteadOfAShell_REQ_WS_005(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh is not installed: %v", err)
	}
	h := newHarness(t)

	// The marker is distinctive so finding it cannot be an accident of the shell's own
	// chatter, and `printf` is used rather than `echo` because it emits nothing else.
	//
	// The command sleeps after printing, and that is not decoration. A process that exits
	// immediately takes its session with it, and the screen goes with the session — so a
	// `printf` alone races the observer and loses, which is how the first version of this
	// test failed while the code was right.
	const marker = "umbral-command-ran-8f2a"
	session, err := h.Create(t.Context(), domain.CreateParams{
		Shell: "/bin/sh", CWD: t.TempDir(), Size: testSize,
		Command: []string{"sh", "-c", "printf " + marker + "; sleep 5"},
		// Asking for integration alongside a command is what a careless caller would do;
		// the service has to ignore it rather than try to inject a bootstrap into sh -c.
		ShellIntegration: true,
	})
	if err != nil {
		t.Fatalf("Create with a command: %v", err)
	}

	if !waitForScreen(t, h, session.ID, marker) {
		t.Fatalf("the command's output never appeared; %q was not written to the screen", marker)
	}

	// The session records the program that is actually running, which is what `session.list`
	// shows a client (API Spec §4's `Session.shell`).
	stored, err := h.Get(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.HasSuffix(stored.Shell, "/sh") {
		t.Errorf("the session records shell %q, want the resolved sh it ran", stored.Shell)
	}
}

// TestACommandPaneGetsNoShellIntegration_REQ_BLK_003: a command session settles on
// `integration: none`, which is the degraded mode REQ-BLK-003 already describes — not a
// failure, and not something the caller has to know to ask for even when it wrongly asks
// for integration.
//
// This proves the state the daemon settles on and nothing more. It cannot prove that no
// bootstrap was attempted: one injected into `sh -c` would emit no OSC 133 either, so the
// session would land here anyway. `TestLaunchRunsTheCommandAndSkipsTheBootstrap_REQ_WS_005`
// in the sessions package is where that half is observable.
func TestACommandPaneGetsNoShellIntegration_REQ_BLK_003(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh is not installed: %v", err)
	}
	h := newHarness(t)

	session, err := h.Create(t.Context(), domain.CreateParams{
		Shell: "/bin/sh", CWD: t.TempDir(), Size: testSize,
		Command: []string{"sh", "-c", "sleep 5"}, ShellIntegration: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The environment marker a bootstrap would have set is the observable difference, and
	// the screen is where a sourced rc file would have announced itself. Neither happens;
	// what is asserted here is the state the daemon settles on.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, err := h.Get(t.Context(), session.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if current.Integration == domain.IntegrationNone {
			return
		}
		if current.Integration == domain.IntegrationOSC133 {
			t.Fatalf("a command session reported shell integration; nothing could have injected it")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("the command session never settled on integration: none")
}

// TestACommandOffThePathIsAValidationError: `layout.apply` replays a layout written
// somewhere else, so "that program is not here" is an ordinary outcome and has to be a
// validation error the client can read — not an internal fault, and not a session that
// starts and dies.
func TestACommandOffThePathIsAValidationError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.Create(t.Context(), domain.CreateParams{
		Shell: "/bin/sh", CWD: t.TempDir(), Size: testSize,
		Command: []string{"umbral-no-such-program-4c1d"},
	})
	if err == nil {
		t.Fatal("a session was created for a program that does not exist")
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("error = %v, want a validation error", err)
	}
}

// TestACommandIsResolvedAgainstTheSessionsOwnPath is the finding that a portable layout may
// declare where its program lives.
//
// `exec.LookPath` reads the daemon's PATH, which is the wrong one: a layout carrying
// `env: {"PATH": "/opt/toolchain/bin"}` beside `command: ["mytool"]` has said exactly where
// to look, and resolving against the daemon's PATH would refuse it.
func TestACommandIsResolvedAgainstTheSessionsOwnPath(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh is not installed: %v", err)
	}
	h := newHarness(t)

	// A directory that is on nobody's PATH, holding a program under a name nothing else
	// has. Finding it can only mean the session's own PATH was used.
	dir := t.TempDir()
	const name = "umbral-probe-tool"
	const marker = "found-on-the-sessions-path-51ba"
	script := "#!/bin/sh\nprintf " + marker + "\nsleep 5\n"
	if err := writeExecutable(t, filepath.Join(dir, name), script); err != nil {
		t.Fatalf("write the probe: %v", err)
	}

	// The probe's directory is prepended to the real PATH rather than replacing it. It
	// still discriminates — the daemon's own PATH does not contain this directory, so
	// finding the probe can only mean the session's PATH was consulted — and it leaves the
	// script able to reach `sleep`, which it needs to stay alive long enough to be read.
	session, err := h.Create(t.Context(), domain.CreateParams{
		Shell: "/bin/sh", CWD: t.TempDir(), Size: testSize,
		Command: []string{name},
		Env:     map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH")},
	})
	if err != nil {
		t.Fatalf("Create with a command on the session's own PATH: %v", err)
	}
	if !waitForScreen(t, h, session.ID, marker) {
		t.Errorf("the probe's output never appeared; %q was not written to the screen", marker)
	}
}
