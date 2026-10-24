package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// fakeWorktrees is the worktree engine as the runtime sees it.
type fakeWorktrees struct {
	mu      sync.Mutex
	root    string // where a created worktree goes
	repo    string // the repository every thread's cwd is in
	active  map[string]ports.Worktree
	created []string
	fail    error
}

func (f *fakeWorktrees) Create(_ context.Context, threadID, cwd, _ string) (ports.Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return ports.Worktree{}, f.fail
	}
	w := ports.Worktree{ID: "wtr_" + threadID, Path: filepath.Join(f.root, threadID), RepoRoot: f.repo}
	if err := os.MkdirAll(filepath.Join(w.Path, "sub"), 0o755); err != nil {
		return ports.Worktree{}, err
	}
	if f.active == nil {
		f.active = map[string]ports.Worktree{}
	}
	f.active[threadID] = w
	f.created = append(f.created, cwd)
	return w, nil
}

func (f *fakeWorktrees) Active(_ context.Context, threadID string) (ports.Worktree, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.active[threadID]
	return w, ok, nil
}

func worktreeRig(t *testing.T) (*rig, *fakeWorktrees) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	wts := &fakeWorktrees{root: t.TempDir(), repo: repo}
	r := newRigWith(t, func(c *Config) { c.Worktrees = wts },
		callTool("write_file", `{"path":"out.txt","content":"x"}`), answer("written"),
		callTool("write_file", `{"path":"out.txt","content":"x"}`), answer("written"))
	return r, wts
}

// TestATurnRunsInItsThreadsWorktree_REQ_WT_002: a thread created with a worktree runs its turns
// there — the tools get the worktree's directory matching the thread's cwd, and the worktree as
// the write root — and the thread records its worktree and its workspace.
func TestATurnRunsInItsThreadsWorktree_REQ_WT_002(t *testing.T) {
	r, wts := worktreeRig(t)
	th := r.thread(t, domain.CreateParams{
		Cwd: filepath.Join(wts.repo, "sub"), Worktree: true, Workspace: "w2", Mode: secdomain.ModeAutoEdit,
	})
	wt := wts.active[th.ID]
	if th.WorktreeID != wt.ID || th.WorkspaceID != "w2" {
		t.Fatalf("thread %+v, want worktree %s in workspace w2", th, wt.ID)
	}
	if stored, _ := r.store.Thread(t.Context(), th.ID); stored.WorktreeID != wt.ID || stored.WorkspaceID != "w2" {
		t.Fatalf("stored thread %+v", stored)
	}
	if len(wts.created) != 1 || wts.created[0] != filepath.Join(wts.repo, "sub") {
		t.Fatalf("the worktree was created from %v", wts.created)
	}

	r.send(t, th.ID, "write it", "")
	env := r.tools.tools["write_file"].lastEnv()
	if env.Cwd != filepath.Join(wt.Path, "sub") || env.WriteRoot != wt.Path {
		t.Fatalf("the tool ran with cwd %q, write root %q; want %q, %q", env.Cwd, env.WriteRoot, filepath.Join(wt.Path, "sub"), wt.Path)
	}

	// Once the worktree is no longer active the thread is back in its own directory.
	wts.mu.Lock()
	delete(wts.active, th.ID)
	wts.mu.Unlock()
	r.send(t, th.ID, "write it again", "")
	env = r.tools.tools["write_file"].lastEnv()
	if env.Cwd != filepath.Join(wts.repo, "sub") {
		t.Fatalf("after the worktree left: cwd %q", env.Cwd)
	}
}

// TestAThreadKeepsWorkingWhenItsWorktreeIsRefused_REQ_WT_008: a thread asked for with a
// worktree that git refuses (not a repository, bare, too old) is created without isolation and
// carries the refusal, so the caller knows; it runs in its own directory. Any other failure of
// the worktree takes the thread back: it is not a refusal the user can act on.
func TestAThreadKeepsWorkingWhenItsWorktreeIsRefused_REQ_WT_008(t *testing.T) {
	r, wts := worktreeRig(t)
	refused := fmt.Errorf("%w: not a git repository", ports.ErrWorktreeRefused)
	wts.fail = refused
	th, err := r.rt.Create(t.Context(), domain.CreateParams{Cwd: wts.repo, Worktree: true})
	if err != nil {
		t.Fatalf("Create = %v, want the thread", err)
	}
	if th.WorktreeID != "" || !errors.Is(th.WorktreeRefusal, refused) {
		t.Fatalf("thread %+v, want no worktree and the refusal", th)
	}
	if list, _ := r.store.Threads(t.Context()); len(list) != 1 {
		t.Fatalf("threads %+v, want the one created", list)
	}

	broken := errors.New("git crashed")
	wts.fail = broken
	if _, err := r.rt.Create(t.Context(), domain.CreateParams{Cwd: wts.repo, Worktree: true}); !errors.Is(err, broken) {
		t.Fatalf("Create = %v, want the failure", err)
	}
	if list, _ := r.store.Threads(t.Context()); len(list) != 1 {
		t.Fatalf("a thread whose worktree failed was kept: %+v", list)
	}
}

// TestAWorktreeCannotChangeUnderARunningTurn_REQ_WT_004: worktree.create, merge and discard run
// only while the thread has no turn running (delta `2026-10-worktree-engine`, decision 19);
// once the turn ends they run.
func TestAWorktreeCannotChangeUnderARunningTurn_REQ_WT_004(t *testing.T) {
	r, wts := worktreeRig(t)
	hold := make(chan struct{})
	r.models.hold = hold
	th := r.thread(t, domain.CreateParams{Cwd: wts.repo, Mode: secdomain.ModeAutoEdit})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	ran := false
	err := r.rt.WhileIdle(t.Context(), th.ID, func() error { ran = true; return nil })
	if !errors.Is(err, domain.ErrTurnRunning) || !errors.Is(err, domain.ErrConflict) || ran {
		t.Fatalf("WhileIdle during a turn = %v (ran %t), want ErrTurnRunning", err, ran)
	}
	if _, err := r.rt.CreateWorktree(t.Context(), th.ID, ""); !errors.Is(err, domain.ErrTurnRunning) {
		t.Fatalf("CreateWorktree during a turn = %v", err)
	}
	if len(wts.created) != 0 {
		t.Fatal("a worktree was created under a running turn")
	}

	close(hold)
	r.finished(t)
	id, err := r.rt.CreateWorktree(t.Context(), th.ID, "")
	if err != nil || id != "wtr_"+th.ID {
		t.Fatalf("CreateWorktree after the turn = %q, %v", id, err)
	}
	if got, _ := r.rt.Get(t.Context(), th.ID); got.WorktreeID != id {
		t.Fatalf("the thread does not record its worktree: %+v", got)
	}
	if err := r.rt.WhileIdle(t.Context(), th.ID, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("WhileIdle after the turn = %v (ran %t)", err, ran)
	}
	if err := r.rt.WhileIdle(t.Context(), "thr_none", func() error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("WhileIdle of an unknown thread = %v", err)
	}
}

// TestAWorktreeChangesAsSoonAsItsTurnEnds_REQ_WT_004: once thread.turn_finished is published
// the turn has ended, so a merge sent the moment a wait returns runs, even though the turn's
// goroutine has not yet let go of it.
func TestAWorktreeChangesAsSoonAsItsTurnEnds_REQ_WT_004(t *testing.T) {
	r, wts := worktreeRig(t)
	th := r.thread(t, domain.CreateParams{Cwd: wts.repo, Mode: secdomain.ModeAutoEdit})
	var errAtEnd error
	r.bus.onFinished = func(ports.TurnFinished) {
		errAtEnd = r.rt.WhileIdle(context.WithoutCancel(t.Context()), th.ID, func() error { return nil })
	}
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	r.finished(t)
	if errAtEnd != nil {
		t.Fatalf("WhileIdle as the turn's end was published = %v, want it to run", errAtEnd)
	}
}

// TestInWorkspaceListsAWorkspacesThreads_REQ_WT_007: a group named by its workspace is the
// threads created in it, ephemeral ones included.
func TestInWorkspaceListsAWorkspacesThreads_REQ_WT_007(t *testing.T) {
	r, wts := worktreeRig(t)
	a := r.thread(t, domain.CreateParams{Cwd: wts.repo, Workspace: "w1"})
	b := r.thread(t, domain.CreateParams{Cwd: wts.repo, Workspace: "w1", Ephemeral: true})
	r.thread(t, domain.CreateParams{Cwd: wts.repo, Workspace: "w2"})
	r.thread(t, domain.CreateParams{Cwd: wts.repo})
	got, err := r.rt.InWorkspace(t.Context(), "w1")
	if err != nil || len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("InWorkspace(w1) = %+v, %v", got, err)
	}
}

// TestATurnsCwdStaysInItsWorktree_REQ_WT_002: the thread's directory maps to the worktree's
// matching one only while that is inside the worktree; a symlink the agent made there, leading
// out, leaves the turn at the worktree's top.
func TestATurnsCwdStaysInItsWorktree_REQ_WT_002(t *testing.T) {
	t.Parallel()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for _, dir := range []string{filepath.Join(repo, "sub"), filepath.Join(repo, "link"), filepath.Join(wt, "sub")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(wt, "link")); err != nil {
		t.Fatal(err)
	}
	w := ports.Worktree{ID: "wtr_1", Path: wt, RepoRoot: repo}
	if got := worktreeCwd(w, filepath.Join(repo, "sub")); got != filepath.Join(wt, "sub") {
		t.Errorf("sub maps to %s", got)
	}
	if got := worktreeCwd(w, filepath.Join(repo, "link")); got != wt {
		t.Errorf("a directory the worktree links out of maps to %s, want the worktree's top %s", got, wt)
	}
	// A worktree path reached through a link — macOS's /var is /private/var — is still inside.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(wt, alias); err != nil {
		t.Fatal(err)
	}
	aliased := ports.Worktree{ID: "wtr_1", Path: alias, RepoRoot: repo}
	if got := worktreeCwd(aliased, filepath.Join(repo, "sub")); got != filepath.Join(alias, "sub") {
		t.Errorf("through a linked worktree path, sub maps to %s", got)
	}
}
