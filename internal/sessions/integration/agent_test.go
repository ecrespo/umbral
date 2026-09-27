package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// thread inserts the thread row a thread's PTY and its blocks hold foreign keys against.
func thread(t *testing.T, h *harness, cwd string) string {
	t.Helper()
	id := store.NewID(store.PrefixThread)
	if _, err := h.store.DB().ExecContext(t.Context(),
		`INSERT INTO threads (id, cwd, created_at, updated_at) VALUES (?, ?, 1, 1)`, id, cwd); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestRunCommandCreatesAgentBlock_REQ_AGT_003: run_command runs in a PTY dedicated to the
// thread — a session whose owner is the thread and whose input belongs to the agent, created
// on first use and reused after — and is recorded as a block with origin agent and the
// thread's id, whose exit code and output come back to the caller.
func TestRunCommandCreatesAgentBlock_REQ_AGT_003(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	run, err := h.RunForThread(ctx, threadID, cwd, `printf 'agent says hi\n'; sh -c 'exit 4'`)
	if err != nil {
		t.Fatal(err)
	}
	b := run.Block
	switch {
	case b.Origin != domain.OriginAgent || b.ThreadID != threadID:
		t.Errorf("block origin %q thread %q, want agent and %s", b.Origin, b.ThreadID, threadID)
	case b.State != domain.BlockFinished || b.ExitCode == nil || *b.ExitCode != 4:
		t.Errorf("block %s exit %v, want finished with 4", b.State, b.ExitCode)
	case !strings.Contains(run.Output, "agent says hi"):
		t.Errorf("output = %q", run.Output)
	case !strings.Contains(b.Command, "agent says hi"):
		t.Errorf("command = %q", b.Command)
	}

	var origin, rowThread, owner, inputOwner string
	if err := h.store.DB().QueryRowContext(t.Context(), `
		SELECT b.origin, b.thread_id, s.owner_thread_id, s.input_owner
		FROM blocks b JOIN sessions s ON s.id = b.session_id WHERE b.id = ?`, b.ID).
		Scan(&origin, &rowThread, &owner, &inputOwner); err != nil {
		t.Fatal(err)
	}
	if origin != "agent" || rowThread != threadID || owner != threadID || inputOwner != "agent" {
		t.Errorf("rows: origin %s thread %s owner %s input %s", origin, rowThread, owner, inputOwner)
	}

	got, err := h.Get(t.Context(), b.SessionID)
	if err != nil || got.OwnerThreadID != threadID || got.InputOwner != domain.InputOwnerAgent {
		t.Errorf("session.get = %+v, %v; want the thread as owner and the agent holding input", got, err)
	}
	listed, err := h.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, sess := range listed {
		if sess.ID == b.SessionID && sess.OwnerThreadID != threadID {
			t.Errorf("session.list shows the thread's PTY with owner %q", sess.OwnerThreadID)
		}
	}

	again, err := h.RunForThread(ctx, threadID, cwd, "printf 'second\\n'")
	if err != nil {
		t.Fatal(err)
	}
	if again.Block.SessionID != b.SessionID {
		t.Error("a second command got a new PTY instead of the thread's")
	}
	if err := h.Input(t.Context(), b.SessionID, []byte("echo human\n"), domain.InputOwnerHuman); !isInputLocked(err) {
		t.Errorf("a human typed into the agent's PTY: %v", err)
	}
	for _, bad := range []string{"", "echo a\necho b", "echo a\r"} {
		if _, err := h.RunForThread(ctx, threadID, cwd, bad); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%q: err = %v, want ErrValidation", bad, err)
		}
	}
}

// TestKillForegroundUnder500ms_REQ_AGT_007: cancelling a running command kills what it launched
// in under 500 ms — its process group, not the thread's shell — and the block closes; the
// same PTY runs the next command.
func TestKillForegroundUnder500ms_REQ_AGT_007(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	type outcome struct {
		run domain.AgentRun
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		run, err := h.RunForThread(runCtx, threadID, cwd, "sleep 3171 & wait")
		done <- outcome{run, err}
	}()
	time.Sleep(500 * time.Millisecond)
	stopped := time.Now()
	stop()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled command did not return")
	}
	if took := time.Since(stopped); took > 500*time.Millisecond {
		t.Errorf("cancel took %v, want under 500 ms", took)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got.err)
	}
	after, err := h.RunForThread(ctx, threadID, cwd, "pgrep -f 'sleep 3171' || echo gone")
	if err != nil {
		t.Fatalf("the thread's PTY did not survive the cancel: %v", err)
	}
	if !strings.Contains(after.Output, "gone") {
		t.Errorf("the command's children outlived the cancel: %q", after.Output)
	}

	// A command in the foreground, with no job of its own.
	fgCtx, fgStop := context.WithCancel(ctx)
	go func() { time.Sleep(500 * time.Millisecond); fgStop() }()
	start := time.Now()
	fg, err := h.RunForThread(fgCtx, threadID, cwd, "sleep 3172")
	if !errors.Is(err, context.Canceled) || time.Since(start) > 1500*time.Millisecond {
		t.Errorf("foreground cancel: err %v after %v", err, time.Since(start))
	}
	if fg.Block.ExitCode == nil || *fg.Block.ExitCode == 0 {
		t.Errorf("the killed command's block = %+v, want a non-zero exit", fg.Block)
	}
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Errorf("the PTY after a foreground cancel: %v", err)
	}

	// A command that ignores SIGTERM is killed 300 ms later, still inside the budget.
	stubCtx, stubStop := context.WithCancel(ctx)
	go func() { time.Sleep(500 * time.Millisecond); stubStop() }()
	start = time.Now()
	if _, err := h.RunForThread(stubCtx, threadID, cwd, "sh -c \"trap '' TERM; sleep 3173\""); !errors.Is(err, context.Canceled) {
		t.Errorf("TERM-ignoring cancel: err %v", err)
	}
	if took := time.Since(start) - 500*time.Millisecond; took > 500*time.Millisecond {
		t.Errorf("a command ignoring SIGTERM took %v to stop", took)
	}
	gone, err := h.RunForThread(ctx, threadID, cwd, "pgrep -f 'sleep 3173' || echo gone")
	if err != nil || !strings.Contains(gone.Output, "gone") {
		t.Errorf("the TERM-ignoring command survived: %q, %v", gone.Output, err)
	}

	// A loop of builtins: no child to kill, so the shell itself is interrupted.
	loopCtx, loopStop := context.WithCancel(ctx)
	go func() { time.Sleep(500 * time.Millisecond); loopStop() }()
	if _, err := h.RunForThread(loopCtx, threadID, cwd, "while :; do :; done"); !errors.Is(err, context.Canceled) {
		t.Errorf("loop cancel: err %v", err)
	}
	after2, err := h.RunForThread(ctx, threadID, cwd, "echo still-here")
	if err != nil || !strings.Contains(after2.Output, "still-here") {
		t.Errorf("the PTY after cancelling a builtin loop: %q, %v", after2.Output, err)
	}
}

// TestACancelBeforeTheCommandStartsStillStopsIt_REQ_AGT_007: a run cancelled before the shell
// has started its command — no OSC 133;C yet — is stopped once it does. Signalled at once,
// the interrupt reached a shell that had not begun the loop and was lost, so the loop ran for
// good and held the PTY: the macOS runner's slowness made that window wide enough to hit.
func TestACancelBeforeTheCommandStartsStillStopsIt_REQ_AGT_007(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if _, err := h.RunForThread(ctx, threadID, cwd, "true"); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"while :; do :; done", "sleep 3174"} {
		early, stop := context.WithCancel(ctx)
		stop()
		if _, err := h.RunForThread(early, threadID, cwd, command); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err %v", command, err)
		}
		next, done := context.WithTimeout(ctx, 10*time.Second)
		after, err := h.RunForThread(next, threadID, cwd, "echo still-here")
		done()
		if err != nil || !strings.Contains(after.Output, "still-here") {
			t.Fatalf("after cancelling %q before it started: %q, %v", command, after.Output, err)
		}
	}
}

// TestParallelFirstUsesShareOnePTY_REQ_AGT_003: two commands of a thread that has no PTY yet,
// started together, end up in one PTY — the thread's — rather than one each.
func TestParallelFirstUsesShareOnePTY_REQ_AGT_003(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	type outcome struct {
		run domain.AgentRun
		err error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			var run domain.AgentRun
			var err error
			for range 200 {
				run, err = h.RunForThread(ctx, threadID, cwd, "true")
				if !errors.Is(err, sessions.ErrAgentBusy) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			results <- outcome{run, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil {
		t.Fatalf("errs: %v, %v", a.err, b.err)
	}
	if a.run.Block.SessionID != b.run.Block.SessionID {
		t.Error("two first uses of one thread made two PTYs")
	}
	if !a.run.Persisted || !b.run.Persisted {
		t.Error("a stored block was reported as not persisted")
	}
}
