package integration_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// isInputLocked reports whether err is the lock refusal of REQ-TERM-008.
func isInputLocked(err error) bool { return errors.Is(err, domain.ErrInputLocked) }

// containsBytes reports whether a VT snapshot contains the given text.
func containsBytes(snapshot []byte, want string) bool {
	return strings.Contains(string(snapshot), want)
}

// eventuallyContains polls the session's screen until the text appears. A shell takes an
// unpredictable moment to echo and run a command, so the alternative is a sleep long
// enough to be slow and short enough to be flaky.
func eventuallyContains(t *testing.T, h *harness, sessionID, want string) bool {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := h.Snapshot(t.Context(), sessionID)
		if err == nil && containsBytes(snapshot.Data, want) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// writeExecutable drops a small script on disk with the execute bit set.
func writeExecutable(t *testing.T, path, body string) error {
	t.Helper()
	return os.WriteFile(path, []byte(body), 0o700)
}

// waitForScreen polls a session's rendered screen until it contains want, or gives up.
//
// Polling rather than subscribing: what is being checked is that a process ran and its
// output reached the emulator, and the emulator's screen is the one place both halves are
// visible at once.
func waitForScreen(t *testing.T, h *harness, sessionID, want string) bool {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := h.Snapshot(t.Context(), sessionID)
		if err == nil && strings.Contains(string(snapshot.Data), want) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// TestMain gives every shell this package starts a home of its own, empty, so none reads the
// developer's rc files: the bash bootstrap sources ~/.bashrc and zsh and fish read theirs, and
// with the real home a session ran the developer's prompt framework. That made a shell's start
// slow enough under `task ci` to miss REQ-BLK-003's 5 s window, and its prompt something no
// assertion could predict (TestTheShellsDoNotReadTheDevelopersHome). A test that needs a
// particular rc file sets HOME itself.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "TestSessionsHome")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create a home for the shells:", err)
		os.Exit(1)
	}
	for k, v := range map[string]string{"HOME": home, "ZDOTDIR": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// Files a non-interactive or POSIX shell would read whatever the home.
	for _, k := range []string{"BASH_ENV", "ENV"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
