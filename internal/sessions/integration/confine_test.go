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
)

// fakeConfiner stands for the sandbox: it says whether a thread's shell is to be confined now
// and "confines" one by putting a variable in its environment through /usr/bin/env, so a
// test can tell from inside the shell which kind it is.
type fakeConfiner struct {
	want atomic.Bool
	fail atomic.Bool

	mu   sync.Mutex
	read [][]string
	cwds []string
}

func (f *fakeConfiner) Confined(context.Context, string, string) bool { return f.want.Load() }

func (f *fakeConfiner) Confine(_ context.Context, _, cwd string, read, argv []string) ([]string, []string, error) {
	if f.fail.Load() {
		return nil, nil, errors.New("this thread cannot be confined")
	}
	f.mu.Lock()
	f.read, f.cwds = append(f.read, read), append(f.cwds, cwd)
	f.mu.Unlock()
	return append([]string{"/usr/bin/env", "UMBRAL_TEST_WRAPPED=yes"}, argv...), []string{"UMBRAL_TEST_ENV=given"}, nil
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
	read, cwds := confiner.read, confiner.cwds
	confiner.mu.Unlock()
	if len(read) != 1 || len(read[0]) != 1 || !strings.HasPrefix(filepath.Base(read[0][0]), "shellinteg-") || cwds[0] != cwd {
		t.Fatalf("the confiner was given read %v in %v, want the shell's one start-up directory in %s", read, cwds, cwd)
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
