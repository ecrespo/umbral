package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// TestFullAutoAsksForEveryCommandOnWindows_REQ_WIN_001: a thread in full-auto inside its active
// worktree still writes there unasked, but a command asks as windows_shell, since the floor
// cannot read a PowerShell or cmd line (delta `2026-10-windows-port`, decision 14), and runs once
// approved.
func TestFullAutoAsksForEveryCommandOnWindows_REQ_WIN_001(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	wts := &fakeWorktrees{root: t.TempDir(), repo: repo}
	r := newRigWith(t, func(c *Config) { c.Worktrees = wts },
		callTool("write_file", `{"path":"out.txt","content":"x"}`), callTool("run", `{"path":"make"}`), answer("done"))
	th := r.thread(t, domain.CreateParams{Cwd: filepath.Join(repo, "sub"), Worktree: true, Mode: secdomain.ModeNormal})
	if _, err := r.rt.Update(t.Context(), th.ID, domain.UpdateParams{Mode: modePtr(secdomain.ModeFullAuto)}); err != nil {
		t.Fatalf("full-auto with an active worktree: %v", err)
	}

	r.sendOnly(t, th.ID)
	a := r.requested(t)
	if a.Tool != "run" || a.Reason != secdomain.ReasonDestructive {
		t.Fatalf("approval %s (%s), want run asked as destructive", a.Tool, a.Reason)
	}
	if r.tools.tools["write_file"].ran.Load() != 1 || r.tools.tools["run"].ran.Load() != 0 {
		t.Fatalf("before the answer: write ran %d, run ran %d; want the write unasked and the run held",
			r.tools.tools["write_file"].ran.Load(), r.tools.tools["run"].ran.Load())
	}
	r.respond(t, a, true)
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s", end.StopReason)
	}
	if r.tools.tools["run"].ran.Load() != 1 {
		t.Error("the approved command did not run")
	}
}
