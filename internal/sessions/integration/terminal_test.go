package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// TestAnACPTerminalIsItsOwnThreadSession_REQ_ACP_005: an ACP terminal runs in a new session
// the thread owns, not in the thread's PTY; its command is a block with origin agent, the
// thread's id and via acp; its output and exit are served while the session lives, a kill
// stops the command and keeps the shell, and a release closes the session.
func TestAnACPTerminalIsItsOwnThreadSession_REQ_ACP_005(t *testing.T) {
	h := newHarness(t)
	cwd := t.TempDir()
	threadID := thread(t, h, cwd)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	term, err := h.StartTerminal(ctx, threadID, cwd, `printf 'from the terminal\n'; sh -c 'exit 3'`)
	if err != nil {
		t.Fatal(err)
	}
	run, err := term.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := run.Block
	if b.Origin != domain.OriginAgent || b.ThreadID != threadID || b.Via != domain.ViaACP {
		t.Errorf("block origin %q thread %q via %q, want agent, %s and acp", b.Origin, b.ThreadID, b.Via, threadID)
	}
	if b.ExitCode == nil || *b.ExitCode != 3 {
		t.Errorf("exit %v, want 3", b.ExitCode)
	}
	out, ended := term.Output()
	if ended == nil || !strings.Contains(out, "from the terminal") {
		t.Errorf("output %q, ended %v; want the command's output and its end", out, ended)
	}
	var via, owner string
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT b.via, s.owner_thread_id FROM blocks b JOIN sessions s ON s.id = b.session_id WHERE b.id = ?`, b.ID).
		Scan(&via, &owner); err != nil {
		t.Fatal(err)
	}
	if via != "acp" || owner != threadID {
		t.Errorf("rows: via %s owner %s, want acp and the thread", via, owner)
	}

	// The thread's own PTY is another session, and running in it leaves the terminal alone.
	own, err := h.RunForThread(ctx, threadID, cwd, "true")
	if err != nil {
		t.Fatal(err)
	}
	if own.Block.SessionID == term.SessionID() || own.Block.Via != domain.ViaShell {
		t.Errorf("the thread's command ran in session %s via %q; the terminal is %s",
			own.Block.SessionID, own.Block.Via, term.SessionID())
	}
	if got, err := h.Get(t.Context(), term.SessionID()); err != nil || got.State != domain.StateAlive {
		t.Errorf("the terminal's session after the thread's command: %+v, %v", got, err)
	}

	// Output is served while the command runs, and a last line without a newline is kept.
	partial, err := h.StartTerminal(ctx, threadID, cwd, `printf 'first\n'; sleep 1; printf 'no newline'`)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { out, ended := partial.Output(); return ended == nil && strings.Contains(out, "first") })
	if _, err := partial.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if out, _ := partial.Output(); !strings.Contains(out, "first\nno newline") {
		t.Errorf("output %q, want both lines, the last without its newline", out)
	}

	// A kill stops the command, not the shell.
	long, err := h.StartTerminal(ctx, threadID, cwd, "sleep 3181")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, long.Started)
	long.Kill()
	killed, err := long.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if killed.Block.ExitCode == nil || *killed.Block.ExitCode == 0 {
		t.Errorf("a killed command exited %v", killed.Block.ExitCode)
	}
	if got, err := h.Get(t.Context(), long.SessionID()); err != nil || got.State != domain.StateAlive {
		t.Errorf("the shell after a kill: %+v, %v", got, err)
	}

	// A release closes the session.
	for _, tm := range []interface {
		Release(context.Context) error
		SessionID() string
	}{term, partial, long} {
		if err := tm.Release(ctx); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			got, err := h.Get(t.Context(), tm.SessionID())
			return errors.Is(err, domain.ErrNotFound) || (err == nil && got.State != domain.StateAlive)
		})
	}
}

// eventually waits up to 30 s for cond.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition never held")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
