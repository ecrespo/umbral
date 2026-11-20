package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// fakeConfiner stands for the sandbox: it says whether a thread's shell is to be confined now
// and "confines" one by putting a variable in its environment through /usr/bin/env, so a
// test can tell from inside the shell which kind it is.
type fakeConfiner struct {
	want atomic.Bool
	fail atomic.Bool
	off  atomic.Bool
	// released counts the shells whose cleanup the service has called.
	released atomic.Int32

	mu      sync.Mutex
	read    [][]string
	threads []string
}

func (f *fakeConfiner) Enforces() bool { return !f.off.Load() }

func (f *fakeConfiner) FullAuto(context.Context, string) bool { return f.want.Load() }

func (f *fakeConfiner) Confine(_ context.Context, threadID string, read, argv []string) ([]string, []string, func() error, error) {
	if f.fail.Load() {
		return nil, nil, nil, errors.New("this thread cannot be confined")
	}
	f.mu.Lock()
	f.read, f.threads = append(f.read, read), append(f.threads, threadID)
	f.mu.Unlock()
	release := func() error {
		f.released.Add(1)
		return nil
	}
	return append([]string{"/usr/bin/env", "UMBRAL_TEST_WRAPPED=yes"}, argv...), []string{"UMBRAL_TEST_ENV=given"}, release, nil
}

// TestAThreadsShellIsReplacedWhenItsConfinementChanges_REQ_SBX_006: a sandbox is taken by a
// process for good, so a shell cannot be confined or released where it runs. When the answer
// to "is this thread's shell confined now" changes, the next command gets a new shell of the
// right kind: launched through the confiner with the variables it gives and the shell's own
// start-up files to read, or launched plainly. While the answer holds, the shell is kept.
func TestAThreadsShellIsReplacedWhenItsConfinementChanges_REQ_SBX_006(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skipf("no /usr/bin/env: %v", err)
	}
	confiner := &fakeConfiner{}
	h := newHarnessForShell(t, "bash", func(cfg *sessions.Config) { cfg.Confiner = confiner })
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	probe := `printf 'wrapped=%s env=%s kept=%s\n' "${UMBRAL_TEST_WRAPPED:-no}" "${UMBRAL_TEST_ENV:-none}" "${KEPT:-no}"; KEPT=yes`
	run := func(want string) string {
		t.Helper()
		r, err := h.RunForThread(ctx, threadID, cwd, probe)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Output, want) {
			t.Fatalf("output %q, want %q", r.Output, want)
		}
		return r.Block.SessionID
	}

	plain := run("wrapped=no env=none kept=no")
	if again := run("wrapped=no env=none kept=yes"); again != plain {
		t.Errorf("an unchanged thread got a new shell: %s then %s", plain, again)
	}

	confiner.want.Store(true)
	confined := run("wrapped=yes env=given kept=no")
	if confined == plain {
		t.Error("the unconfined shell was kept for a thread that is now confined")
	}
	if again := run("wrapped=yes env=given kept=yes"); again != confined {
		t.Errorf("a confined thread got a new shell with nothing changed: %s then %s", confined, again)
	}
	confiner.mu.Lock()
	read, threads := confiner.read, confiner.threads
	confiner.mu.Unlock()
	if len(read) != 1 || len(read[0]) != 1 || !strings.HasPrefix(filepath.Base(read[0][0]), "shellinteg-") || threads[0] != threadID {
		t.Fatalf("the confiner was given read %v for %v, want the shell's one start-up directory for %s", read, threads, threadID)
	}
	if info, err := os.Stat(read[0][0]); err != nil || !info.IsDir() {
		t.Errorf("the start-up directory handed to the confiner is not there: %v", err)
	}

	confiner.want.Store(false)
	released := run("wrapped=no env=none kept=no")
	if released == confined || released == plain {
		t.Errorf("leaving confinement did not start a new shell: %s", released)
	}
}

// TestAShellThatCannotBeConfinedDoesNotRun_REQ_SBX_006: when the confiner refuses, the command
// is not run in an unconfined shell instead: it fails, and nothing is left running.
func TestAShellThatCannotBeConfinedDoesNotRun_REQ_SBX_006(t *testing.T) {
	confiner := &fakeConfiner{}
	confiner.want.Store(true)
	confiner.fail.Store(true)
	h := newHarnessForShell(t, "bash", func(cfg *sessions.Config) { cfg.Confiner = confiner })
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	marker := filepath.Join(cwd, "ran")
	if _, err := h.RunForThread(ctx, threadID, cwd, "touch "+marker); err == nil || !strings.Contains(err.Error(), "cannot be confined") {
		t.Fatalf("RunForThread = %v, want the confiner's refusal", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although its shell could not be confined")
	}
}

// TestATurnsOwnFullAutoConfinesWhateverTheRecordSays_REQ_SBX_006: a turn reads its mode once
// and lets its commands run unasked on that reading. Its shell is confined on the same one:
// when the thread's record no longer says full-auto — or a command made it read otherwise —
// a command of that turn still gets the confined shell, and never the plain one a command of
// another turn left. Where no sandbox enforces, neither reading confines anything.
func TestATurnsOwnFullAutoConfinesWhateverTheRecordSays_REQ_SBX_006(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skipf("no /usr/bin/env: %v", err)
	}
	confiner := &fakeConfiner{}
	h := newHarnessForShell(t, "bash", func(cfg *sessions.Config) { cfg.Confiner = confiner })
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	probe := `printf 'wrapped=%s\n' "${UMBRAL_TEST_WRAPPED:-no}"`
	run := func(ctx context.Context, want string) {
		t.Helper()
		r, err := h.RunForThread(ctx, threadID, cwd, probe)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Output, want) {
			t.Fatalf("output %q, want %q", r.Output, want)
		}
	}
	run(ctx, "wrapped=no")
	// The record says no; the turn says full-auto.
	run(domain.WithFullAuto(ctx), "wrapped=yes")
	run(domain.WithFullAuto(ctx), "wrapped=yes")
	// Another turn, not in full-auto, with the record still saying no.
	run(ctx, "wrapped=no")

	confiner.off.Store(true)
	run(domain.WithFullAuto(ctx), "wrapped=no")
	confiner.want.Store(true)
	run(ctx, "wrapped=no")
}

// TestAHostedTerminalIsConfinedUnderFullAuto_REQ_SBX_007: an ACP terminal is a session of its
// own, started by the daemon outside the agent's sandbox. Under `full-auto` its command runs
// unasked, so its shell is launched through the confiner — on the turn's own reading or the
// thread's record — and one that cannot be confined is not started. In any other mode each
// command was a decision, and the terminal is launched plainly.
func TestAHostedTerminalIsConfinedUnderFullAuto_REQ_SBX_007(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skipf("no /usr/bin/env: %v", err)
	}
	confiner := &fakeConfiner{}
	h := newHarnessForShell(t, "bash", func(cfg *sessions.Config) { cfg.Confiner = confiner })
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	marker := filepath.Join(cwd, "ran")
	probe := `printf 'wrapped=%s env=%s\n' "${UMBRAL_TEST_WRAPPED:-no}" "${UMBRAL_TEST_ENV:-none}"; : > ` + marker
	run := func(ctx context.Context, want string) {
		t.Helper()
		term, err := h.StartTerminal(ctx, threadID, cwd, probe)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := term.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if out, _ := term.Output(); !strings.Contains(out, want) {
			t.Fatalf("output %q, want %q", out, want)
		}
		if err := term.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	run(ctx, "wrapped=no env=none")
	// The turn's own reading.
	run(domain.WithFullAuto(ctx), "wrapped=yes env=given")
	// The thread's record.
	confiner.want.Store(true)
	run(ctx, "wrapped=yes env=given")
	confiner.mu.Lock()
	threads, read := confiner.threads, confiner.read
	confiner.mu.Unlock()
	if len(threads) != 2 || threads[0] != threadID || len(read[0]) != 1 {
		t.Errorf("the confiner was asked for %v with %v, want this thread twice with its start-up directory", threads, read)
	}
	// What the confiner made for a terminal goes when the terminal is released.
	for deadline := time.Now().Add(20 * time.Second); confiner.released.Load() != 2; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of the 2 confined terminals were cleaned up after their sessions ended", confiner.released.Load())
		}
	}

	// A terminal that cannot be confined is not started, and nothing of its command runs.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	confiner.fail.Store(true)
	if term, err := h.StartTerminal(domain.WithFullAuto(ctx), threadID, cwd, probe); err == nil {
		_, _ = term.Wait(ctx)
		t.Error("a terminal that could not be confined was started")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the command ran in a terminal that could not be confined: %v", err)
	}

	// Where no sandbox enforces, nothing is confined.
	confiner.fail.Store(false)
	confiner.off.Store(true)
	run(domain.WithFullAuto(ctx), "wrapped=no env=none")
}
