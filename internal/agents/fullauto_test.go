package agents

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

func modePtr(m secdomain.Mode) *secdomain.Mode { return &m }

// TestFullAutoOnlyInsideAWorktree_REQ_ORC_004: a thread without an active worktree is refused
// full-auto, and so is a thread.create asking for it; a thread with one enters it, keeps its own
// mode beside the flag, and its turn writes and runs inside the worktree without asking. Once the
// worktree is no longer active the next turn decides under the thread's own mode again.
func TestFullAutoOnlyInsideAWorktree_REQ_ORC_004(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	wts := &fakeWorktrees{root: t.TempDir(), repo: repo}
	r := newRigWith(t, func(c *Config) { c.Worktrees = wts },
		callTool("write_file", `{"path":"out.txt","content":"x"}`), callTool("run", `{"path":"make"}`), answer("done"),
		callTool("write_file", `{"path":"out.txt","content":"x"}`), answer("asked"))

	if _, err := r.rt.Create(t.Context(), domain.CreateParams{Cwd: repo, Mode: secdomain.ModeFullAuto}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("thread.create with full-auto: %v, want a validation error", err)
	}

	plain := r.thread(t, domain.CreateParams{Cwd: repo, Mode: secdomain.ModeNormal})
	if _, err := r.rt.Update(t.Context(), plain.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("full-auto without a worktree: %v, want a validation error", err)
	}
	if got, _ := r.store.Thread(t.Context(), plain.ID); got.FullAuto {
		t.Fatal("a refused full-auto was stored")
	}

	th := r.thread(t, domain.CreateParams{Cwd: filepath.Join(repo, "sub"), Worktree: true, Mode: secdomain.ModeNormal})
	got, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)})
	if err != nil {
		t.Fatalf("full-auto with an active worktree: %v", err)
	}
	if !got.FullAuto || got.Mode != secdomain.ModeNormal {
		t.Fatalf("thread %+v: want full_auto set beside its own mode normal", got)
	}

	// What full-auto runs unasked is a POSIX line; on Windows every line asks (decision 14),
	// which TestFullAutoAsksForEveryCommandOnWindows_REQ_WIN_001 proves instead.
	posixLines(t)
	if _, end := r.send(t, th.ID, "build it", ""); end.StopReason != domain.StopEndTurn {
		t.Fatalf("the turn ended %q, want end_turn without an approval", end.StopReason)
	}
	if r.tools.tools["write_file"].ran.Load() != 1 || r.tools.tools["run"].ran.Load() != 1 {
		t.Fatalf("write ran %d, run ran %d; want both once, unasked",
			r.tools.tools["write_file"].ran.Load(), r.tools.tools["run"].ran.Load())
	}

	// The worktree leaves `active`: the flag no longer applies, and the thread's own mode,
	// normal, decides again, so the write asks.
	wts.mu.Lock()
	delete(wts.active, th.ID)
	wts.mu.Unlock()
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "again"}); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-r.bus.approvals:
		if a.Tool != "write_file" {
			t.Fatalf("approval for %s, want write_file", a.Tool)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a write outside a worktree ran without asking after the worktree left active")
	}
}
