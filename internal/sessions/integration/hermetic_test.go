package integration_test

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// TestTheShellsDoNotReadTheDevelopersHome: the shells this package starts read the rc files of
// a home of their own, never the developer's. The bash bootstrap sources ~/.bashrc, so with the
// real home every session ran the developer's prompt framework — slow enough under a loaded
// `task ci` to miss REQ-BLK-003's 5 s window, and a prompt no assertion could predict.
func TestTheShellsDoNotReadTheDevelopersHome(t *testing.T) {
	t.Parallel()
	real, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	h := newHarness(t)
	session := h.create(t, domain.CreateParams{ShellIntegration: true})
	if err := h.Input(t.Context(), session.ID, []byte("printf 'home=[%s]\\n' \"$HOME\"\n"), domain.InputOwnerHuman); err != nil {
		t.Fatal(err)
	}
	if !waitForScreen(t, h, session.ID, "home=[/") {
		t.Fatal("the shell never printed its home")
	}
	text, _, _, err := h.ScreenText(t.Context(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "home=["+real.HomeDir+"]") {
		t.Fatalf("the session's shell runs in the developer's home %s:\n%s", real.HomeDir, text)
	}
}

// TestASlowThreadShellStillRunsCommands_REQ_BLK_003: a thread shell whose first prompt comes
// after REQ-BLK-003's 5 s window is marked `none` first and promoted to `osc133` when its
// markers arrive — "the window is a heuristic about silence, not a verdict about the shell".
// run_command must wait for that promotion rather than fail on the provisional `none`, or a
// loaded machine makes the agent's commands fail for good (REQ-AGT-003).
//
// Not parallel: it gives the package's shells a home whose ~/.bashrc takes 6 s.
func TestASlowThreadShellStillRunsCommands_REQ_BLK_003(t *testing.T) {
	// Whichever shell $SHELL names: each reads its own rc file from the home.
	home := t.TempDir()
	for _, rc := range []string{".bashrc", ".zshrc", ".config/fish/config.fish"} {
		path := filepath.Join(home, rc)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("sleep 6\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)

	started := time.Now()
	run, err := h.RunForThread(t.Context(), threadID, cwd, "printf 'late but here\\n'")
	if err != nil {
		t.Fatalf("after %s: %v", time.Since(started).Round(time.Millisecond), err)
	}
	if time.Since(started) < domain.IntegrationWindow {
		t.Fatalf("the shell answered in %s; the test needs it slower than the %s window", time.Since(started), domain.IntegrationWindow)
	}
	if run.Block.Origin != domain.OriginAgent || !strings.Contains(run.Output, "late but here") {
		t.Fatalf("run = %+v", run)
	}
	got, err := h.Get(t.Context(), run.Block.SessionID)
	if err != nil || got.Integration != domain.IntegrationOSC133 {
		t.Fatalf("the session's integration is %q, %v; want osc133 after the late markers", got.Integration, err)
	}
}
