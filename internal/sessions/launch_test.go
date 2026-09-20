package sessions

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// These are in the package rather than beside the integration tests because what they check
// is a decision, not an outcome: which program the child will be, and whether a bootstrap
// was assembled for it. From outside, a session that ignored its command and a session whose
// command happened to produce nothing look identical.

// TestLaunchRunsTheCommandAndSkipsTheBootstrap_REQ_WS_005: a command replaces the shell, and
// nothing is injected alongside it.
//
// The integration test proves the command's output reaches the screen. What it cannot prove
// is the second half: a bootstrap injected into `sh -c` would emit no OSC 133 either, so the
// session would still settle on `integration: none` and the test would still pass. This is
// where "no bootstrap" is actually observable.
func TestLaunchRunsTheCommandAndSkipsTheBootstrap_REQ_WS_005(t *testing.T) {
	t.Parallel()

	s := &Service{cfg: Config{
		Bootstrap: refusingBootstrap{t: t},
		Logger:    slog.New(slog.DiscardHandler),
	}}

	program, args, env, cleanup, err := s.launch(domain.CreateParams{
		Shell:            "/bin/sh",
		Command:          []string{"sh", "-c", "true"},
		ShellIntegration: true,
		Env:              map[string]string{"UMBRAL_ROLE": "tests"},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !strings.HasSuffix(program, "/sh") {
		t.Errorf("program = %q, want the resolved sh", program)
	}
	if len(args) != 2 || args[0] != "-c" || args[1] != "true" {
		t.Errorf("args = %v, want the command's own arguments", args)
	}
	if cleanup != nil {
		t.Error("launch returned a bootstrap cleanup for a command session")
	}
	if !hasEnv(env, "UMBRAL_ROLE=tests") {
		t.Errorf("env %v does not carry the caller's override", truncate(env))
	}
}

// refusingBootstrap fails the test if it is asked to prepare anything. A command session
// must not reach it at all — `Prepare` is what would inject an rc file into the child.
type refusingBootstrap struct{ t *testing.T }

func (b refusingBootstrap) Prepare(shell string) ([]string, []string, func() error, error) {
	b.t.Errorf("the bootstrap was asked to prepare %q for a session that carries a command", shell)
	return nil, nil, nil, nil
}

// TestLaunchResolvesAgainstTheSessionsPath pins the resolution rule: a portable layout may
// declare where its program lives, and the daemon's own PATH is not that place.
func TestLaunchResolvesAgainstTheSessionsPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const name = "umbral-launch-probe"
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write the probe: %v", err)
	}

	s := &Service{cfg: Config{Logger: slog.New(slog.DiscardHandler)}}
	program, _, _, _, err := s.launch(domain.CreateParams{
		Command: []string{name},
		Env:     map[string]string{"PATH": dir},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if program != target {
		t.Errorf("program = %q, want %q resolved from the session's own PATH", program, target)
	}
}

// TestLookPathRefusesWhatItCannotRun covers the branches that turn a bad command into a
// validation error a client can read, rather than a session that starts and dies.
func TestLookPathRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a-directory"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	env := []string{"PATH=" + dir}
	for name, probe := range map[string]string{
		"a name that is nowhere":      "umbral-absent-program",
		"a file without the exec bit": "not-executable",
		"a directory":                 "a-directory",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := lookPath(probe, env); err == nil {
				t.Errorf("lookPath(%q) succeeded", probe)
			}
		})
	}

	// A name carrying a separator is a path already; the adapter validates it.
	if got, err := lookPath("/bin/sh", nil); err != nil || got != "/bin/sh" {
		t.Errorf("lookPath(\"/bin/sh\") = %q, %v; want it returned unchanged", got, err)
	}
}

// TestPathFromTakesTheLastAssignment: `environ` appends the caller's overrides after the
// daemon's own environment, and execve resolves duplicates by taking the last. Reading the
// first would silently use the daemon's PATH while looking like it used the session's.
func TestPathFromTakesTheLastAssignment(t *testing.T) {
	t.Parallel()

	got := pathFrom([]string{"PATH=/daemon/bin", "HOME=/root", "PATH=/session/bin"})
	if got != "/session/bin" {
		t.Errorf("pathFrom = %q, want the last assignment /session/bin", got)
	}
	if pathFrom([]string{"HOME=/root"}) != "" {
		t.Error("pathFrom invented a PATH where the environment has none")
	}
}

func hasEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

// truncate keeps a failure message readable: the child's environment is the daemon's whole
// one plus the overrides, and printing it in full buries the assertion.
func truncate(env []string) []string {
	if len(env) <= 4 {
		return env
	}
	return append(env[len(env)-4:], "…")
}

// TestAnEmptyPathElementIsNotTheWorkingDirectory is a security property, not a formatting
// detail.
//
// POSIX reads an empty element of PATH as the working directory, which turns "run mytool"
// into "run whatever is called mytool in the directory this pane opens in". A layout carries
// both the PATH and the cwd, so honouring it would let a shared layout file execute a binary
// that happened to be sitting in a checkout. The element is skipped, and the search carries
// on to the next one rather than stopping there.
//
// It cannot be parallel: it changes the process's working directory, which every other test
// in this package shares.
func TestAnEmptyPathElementIsNotTheWorkingDirectory(t *testing.T) {
	here := t.TempDir()
	elsewhere := t.TempDir()

	// The same name in both places. One is the decoy in the working directory; the other
	// is the one a correctly-resolved lookup finds.
	const name = "umbral-ambiguous-tool"
	decoy := filepath.Join(here, name)
	real := filepath.Join(elsewhere, name)
	for _, path := range []string{decoy, real} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Chdir(here)

	got, err := lookPath(name, []string{"PATH=:" + elsewhere})
	if err != nil {
		t.Fatalf("lookPath: %v", err)
	}
	if got == decoy || got == name {
		t.Fatalf("lookPath resolved to %q, the binary in the working directory; an empty PATH "+
			"element must not mean \".\"", got)
	}
	if got != real {
		t.Errorf("lookPath = %q, want %q", got, real)
	}
}
