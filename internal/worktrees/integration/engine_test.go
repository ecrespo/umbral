package integration

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/worktrees"
	"github.com/ecrespo/umbral/internal/worktrees/adapters/gitcli"
	"github.com/ecrespo/umbral/internal/worktrees/domain"
	"github.com/ecrespo/umbral/internal/worktrees/ports"
)

// memStore is the Store port in memory, so the engine's tests need no database; the SQLite
// adapter has its own tests.
type memStore struct {
	mu   sync.Mutex
	rows map[string]domain.Worktree
}

func newMemStore() *memStore { return &memStore{rows: map[string]domain.Worktree{}} }

func (m *memStore) Insert(_ context.Context, w domain.Worktree) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.ThreadID == w.ThreadID && r.State == domain.StateActive {
			return errors.New("a second active worktree")
		}
	}
	m.rows[w.ID] = w
	return nil
}

func (m *memStore) Get(_ context.Context, id string) (domain.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.rows[id]
	if !ok {
		return domain.Worktree{}, domain.ErrNotFound
	}
	return w, nil
}

func (m *memStore) Active(_ context.Context, threadID string) (domain.Worktree, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.ThreadID == threadID && r.State == domain.StateActive {
			return r, true, nil
		}
	}
	return domain.Worktree{}, false, nil
}

func (m *memStore) ListActive(context.Context) ([]domain.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Worktree
	for _, r := range m.rows {
		if r.State == domain.StateActive {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) List(_ context.Context, threadIDs []string) ([]domain.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Worktree
	for _, r := range m.rows {
		if threadIDs == nil || slices.Contains(threadIDs, r.ThreadID) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b domain.Worktree) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *memStore) SetState(_ context.Context, id string, s domain.State, at int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	w.State, w.UpdatedAt = s, at
	m.rows[id] = w
	return nil
}

// gitEnv isolates every git the test runs, and the engine's, from the developer's own
// configuration: no global or system file, a fixed identity, no signing unless a test asks.
func gitEnv(t *testing.T) []string {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"GIT_CONFIG_GLOBAL=" + global, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
}

type fixture struct {
	t    *testing.T
	env  []string
	repo string // the main working copy
	data string // where worktrees go
	st   *memStore
	svc  *worktrees.Service
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := &fixture{t: t, env: gitEnv(t), repo: filepath.Join(t.TempDir(), "repo"), data: t.TempDir(), st: newMemStore()}
	if err := os.MkdirAll(f.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo, "init", "-q", "-b", "main")
	f.write(f.repo, "a.txt", "one\ntwo\nthree\n")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", "first")
	f.svc = f.service(2 * time.Second)
	return f
}

func (f *fixture) service(commitTimeout time.Duration) *worktrees.Service {
	return worktrees.New(worktrees.Options{
		Store: f.st, Git: gitcli.New(gitcli.Options{Env: f.env}), Root: f.data,
		CommitTimeout: commitTimeout,
		NewID:         func() string { f.n++; return fmt.Sprintf("wtr_%d", f.n) },
		Now:           func() time.Time { return time.UnixMilli(1000) },
	})
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.CommandContext(f.t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), f.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(dir, name, body string) {
	f.t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(dir, name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) create(thread string) domain.Worktree {
	f.t.Helper()
	w, err := f.svc.Create(f.t.Context(), worktrees.CreateRequest{ThreadID: thread, Cwd: f.repo})
	if err != nil {
		f.t.Fatalf("Create: %v", err)
	}
	return w
}

// state is what "left as it was" compares: HEAD, the tracked status and every file's content.
func (f *fixture) state(dir string) string {
	f.t.Helper()
	var b strings.Builder
	b.WriteString(f.git(dir, "rev-parse", "HEAD") + "\n" + f.git(dir, "status", "--porcelain", "--untracked-files=all") + "\n")
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			body, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			fmt.Fprintf(&b, "%s=%q\n", rel, body)
		}
		return nil
	})
	return b.String()
}

func wantRefusal(t *testing.T, err error, reason domain.Reason) *domain.Refusal {
	t.Helper()
	var r *domain.Refusal
	if !errors.As(err, &r) || r.Reason != reason {
		t.Fatalf("err = %v, want a %s refusal", err, reason)
	}
	return r
}

// TestWorktreeCreateIsOutsideTheRepo_REQ_WT_001: a worktree is created on `umbral/<thread>` from
// HEAD under <root>/<repo_id>/<thread>, outside the repository: the repository's status does not
// see it, and the record carries the repository, base commit, branch, path and thread.
func TestWorktreeCreateIsOutsideTheRepo_REQ_WT_001(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")

	common, err := filepath.EvalSymlinks(filepath.Join(f.repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.data, domain.RepoID(common), "thr_a"); w.Path != want {
		t.Errorf("path %s, want %s", w.Path, want)
	}
	if rel, err := filepath.Rel(f.repo, w.Path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("the worktree %s is inside the repository", w.Path)
	}
	head := f.git(f.repo, "rev-parse", "HEAD")
	if w.BaseCommit != head || w.Branch != "umbral/thr_a" || w.OriginBranch != "main" || w.ThreadID != "thr_a" ||
		w.State != domain.StateActive || w.RepoID != domain.RepoID(common) {
		t.Errorf("record %+v", w)
	}
	if got := f.git(w.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "umbral/thr_a" {
		t.Errorf("the worktree is on %s", got)
	}
	if f.read(w.Path, "a.txt") != "one\ntwo\nthree\n" {
		t.Error("the worktree does not hold the base commit's files")
	}
	if st := f.git(f.repo, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the repository's status sees the worktree: %q", st)
	}
	if got, err := f.st.Get(t.Context(), w.ID); err != nil || got != w {
		t.Errorf("stored %+v (%v), returned %+v", got, err, w)
	}
	// A second worktree for the same thread is refused while the first is active.
	_, err = f.svc.Create(t.Context(), worktrees.CreateRequest{ThreadID: "thr_a", Cwd: f.repo})
	wantRefusal(t, err, domain.ReasonExists)
	// A given base is used instead of HEAD.
	f.write(f.repo, "a.txt", "changed\n")
	f.git(f.repo, "commit", "-qam", "second")
	b, err := f.svc.Create(t.Context(), worktrees.CreateRequest{ThreadID: "thr_b", Cwd: f.repo, Base: head})
	if err != nil || b.BaseCommit != head || f.read(b.Path, "a.txt") != "one\ntwo\nthree\n" {
		t.Errorf("Create from a base: %+v %v", b, err)
	}
}

// TestWorktreeDiffHasStableHunks_REQ_WT_003: the diff runs from the base commit to the working
// tree, untracked files included, and a hunk keeps its id while the worktree does not change it.
func TestWorktreeDiffHasStableHunks_REQ_WT_003(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "one\nTWO\nthree\n")
	f.write(w.Path, "new/b.txt", "brand new\n")

	firstDiff, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := firstDiff.Files
	if len(first) != 2 || first[0].Path != "a.txt" || first[1].Path != "new/b.txt" || first[1].Status != "added" {
		t.Fatalf("diff %+v, want a.txt modified and new/b.txt added", first)
	}
	if len(first[0].Hunks) != 1 || !strings.Contains(strings.Join(first[0].Hunks[0].Lines, "\n"), "+TWO") {
		t.Fatalf("a.txt's hunk %+v", first[0].Hunks)
	}
	againDiff, err := f.svc.Diff(t.Context(), w.ID)
	again := againDiff.Files
	if err != nil || againDiff.Revision != firstDiff.Revision || again[0].Hunks[0].ID != first[0].Hunks[0].ID || again[1].Hunks[0].ID != first[1].Hunks[0].ID {
		t.Errorf("the ids changed between two diffs of the same tree: %v", err)
	}
	// Reading the diff staged nothing in the worktree.
	if st := f.git(w.Path, "diff", "--cached", "--name-only"); st != "" {
		t.Errorf("Diff left %q staged", st)
	}
}

// TestWorktreeMergeSquashesIntoTheOriginBranch_REQ_WT_004: a merge makes one commit on the origin
// branch holding every change of the worktree — committed in it or not, untracked included —
// without checking out another branch, and marks the worktree merged.
func TestWorktreeMergeSquashesIntoTheOriginBranch_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "one\nTWO\nthree\n")
	f.git(w.Path, "commit", "-qam", "inside the worktree")
	f.write(w.Path, "b.txt", "untracked\n")
	before := f.git(f.repo, "rev-parse", "HEAD")

	res, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Message: "squash thr_a"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if parent := f.git(f.repo, "rev-parse", "HEAD~1"); parent != before {
		t.Errorf("the merge is not one commit on top of main: parent %s, want %s", parent, before)
	}
	if res.Commit != f.git(f.repo, "rev-parse", "HEAD") || f.git(f.repo, "log", "-1", "--format=%s") != "squash thr_a" {
		t.Errorf("result %+v, HEAD %s", res, f.git(f.repo, "log", "-1", "--oneline"))
	}
	if f.git(f.repo, "rev-parse", "--abbrev-ref", "HEAD") != "main" {
		t.Error("the main copy is no longer on main")
	}
	if f.read(f.repo, "a.txt") != "one\nTWO\nthree\n" || f.read(f.repo, "b.txt") != "untracked\n" {
		t.Error("the commit does not hold the worktree's changes")
	}
	if st := f.git(f.repo, "status", "--porcelain"); st != "" {
		t.Errorf("the main copy is left dirty: %q", st)
	}
	got, _ := f.st.Get(t.Context(), w.ID)
	if got.State != domain.StateMerged {
		t.Errorf("state %s, want merged", got.State)
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the merged worktree's directory is still there")
	}
}

// TestWorktreeMergeRefusesADirtyCopy_REQ_WT_004: a main copy with uncommitted changes refuses the
// merge with `dirty` and nothing changes on either side.
func TestWorktreeMergeRefusesADirtyCopy_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "worktree\n")
	f.write(f.repo, "a.txt", "user's own edit\n")
	main, wt := f.state(f.repo), f.state(w.Path)
	_, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	wantRefusal(t, err, domain.ReasonDirty)
	if f.state(f.repo) != main || f.state(w.Path) != wt {
		t.Error("a refused merge changed a side")
	}
}

// TestWorktreeMergeRefusesAnotherBranch_REQ_WT_004: a main copy on another branch than the
// worktree's origin refuses with `other_branch`.
func TestWorktreeMergeRefusesAnotherBranch_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "worktree\n")
	f.git(f.repo, "switch", "-q", "-c", "feature")
	main := f.state(f.repo)
	_, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	wantRefusal(t, err, domain.ReasonOtherBranch)
	if f.state(f.repo) != main {
		t.Error("a refused merge changed the main copy")
	}
}

// TestWorktreeMergeRefusesADetachedHead_REQ_WT_004: a main copy on a detached HEAD refuses with
// `detached`.
func TestWorktreeMergeRefusesADetachedHead_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "worktree\n")
	f.git(f.repo, "switch", "-q", "--detach", "HEAD")
	main := f.state(f.repo)
	_, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	wantRefusal(t, err, domain.ReasonDetached)
	if f.state(f.repo) != main {
		t.Error("a refused merge changed the main copy")
	}
}

// TestWorktreeMergeConflictLeavesBothSides_REQ_WT_004: a squash that conflicts is aborted, both
// copies are left as they were, the worktree stays active, and the refusal names the paths.
func TestWorktreeMergeConflictLeavesBothSides_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "one\nWORKTREE\nthree\n")
	f.write(w.Path, "c.txt", "a new file\n")
	f.write(f.repo, "a.txt", "one\nMAIN\nthree\n")
	f.git(f.repo, "commit", "-qam", "main moved on")
	main, wt := f.state(f.repo), f.state(w.Path)

	_, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	r := wantRefusal(t, err, domain.ReasonConflict)
	if len(r.Paths) != 1 || r.Paths[0] != "a.txt" {
		t.Errorf("paths %v, want [a.txt]", r.Paths)
	}
	if f.state(f.repo) != main {
		t.Errorf("the main copy was changed:\n%s\nwant\n%s", f.state(f.repo), main)
	}
	if f.state(w.Path) != wt {
		t.Error("the worktree was changed")
	}
	if got, _ := f.st.Get(t.Context(), w.ID); got.State != domain.StateActive {
		t.Errorf("state %s after a conflict, want active", got.State)
	}
}

// TestARejectedCommitLeavesBothSides_REQ_WT_004: a pre-commit hook that rejects the commit, and a
// signing step that waits for a passphrase it cannot get, end the merge with `commit_failed` and
// git's output, within the commit timeout, leaving both copies as they were.
func TestARejectedCommitLeavesBothSides_REQ_WT_004(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(f *fixture){
		"a rejecting hook": func(f *fixture) {
			f.write(f.repo, ".git/hooks/pre-commit", "#!/bin/sh\necho 'lint failed: a.txt' >&2\nexit 1\n")
			if err := os.Chmod(filepath.Join(f.repo, ".git/hooks/pre-commit"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		},
		"a signing prompt": func(f *fixture) {
			gpg := filepath.Join(f.t.TempDir(), "gpg")
			// A pinentry that never answers: it waits on standard input, then sleeps.
			f.write(filepath.Dir(gpg), "gpg", "#!/bin/sh\nread pass\nsleep 30\n")
			if err := os.Chmod(gpg, 0o755); err != nil {
				f.t.Fatal(err)
			}
			f.git(f.repo, "config", "commit.gpgsign", "true")
			f.git(f.repo, "config", "gpg.program", gpg)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.svc = f.service(2 * time.Second)
			w := f.create("thr_a")
			f.write(w.Path, "a.txt", "worktree\n")
			f.write(w.Path, "d.txt", "new\n")
			setup(f)
			main, wt := f.state(f.repo), f.state(w.Path)

			start := time.Now()
			_, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
			r := wantRefusal(t, err, domain.ReasonCommitFailed)
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("the merge took %s, past the 2 s commit timeout", elapsed)
			}
			if name == "a rejecting hook" && !strings.Contains(r.Output, "lint failed") {
				t.Errorf("output %q does not carry git's error", r.Output)
			}
			if len(r.Output) > 4096 {
				t.Errorf("output is %d bytes, want at most 4 KiB", len(r.Output))
			}
			if f.state(f.repo) != main {
				t.Errorf("the main copy was changed:\n%s\nwant\n%s", f.state(f.repo), main)
			}
			if f.state(w.Path) != wt {
				t.Error("the worktree was changed")
			}
			if got, _ := f.st.Get(t.Context(), w.ID); got.State != domain.StateActive {
				t.Errorf("state %s, want active", got.State)
			}
		})
	}
}

// TestWorktreeDiscardNeedsConfirm_REQ_WT_005: a discard without confirmation is refused and
// changes nothing; with it, the directory and the branch go and the record stays, discarded.
func TestWorktreeDiscardNeedsConfirm_REQ_WT_005(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "unmerged work\n")

	wantRefusal(t, f.svc.Discard(t.Context(), w.ID, false), domain.ReasonConfirmRequired)
	if f.read(w.Path, "a.txt") != "unmerged work\n" {
		t.Fatal("an unconfirmed discard touched the worktree")
	}
	if err := f.svc.Discard(t.Context(), w.ID, true); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the directory is still there")
	}
	if out := f.git(f.repo, "branch", "--list", w.Branch); out != "" {
		t.Errorf("the branch is still there: %q", out)
	}
	if got, err := f.st.Get(t.Context(), w.ID); err != nil || got.State != domain.StateDiscarded {
		t.Errorf("record %+v (%v), want discarded", got, err)
	}
	if f.git(f.repo, "rev-parse", "--abbrev-ref", "HEAD") != "main" {
		t.Error("the main copy left main")
	}
	wantRefusal(t, f.svc.Discard(t.Context(), w.ID, true), domain.ReasonNotActive)
}

// TestAMissingWorktreeIsMarked_REQ_WT_006: a worktree whose directory or branch was removed
// outside Umbral is marked missing on access and at start, and nothing is recreated.
func TestAMissingWorktreeIsMarked_REQ_WT_006(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gone := f.create("thr_dir")
	branchless := f.create("thr_branch")
	kept := f.create("thr_kept")

	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	// Removing the branch takes detaching the worktree from it first, as a user would.
	f.git(branchless.Path, "switch", "-q", "--detach")
	f.git(f.repo, "branch", "-D", branchless.Branch)

	_, err := f.svc.Diff(t.Context(), gone.ID)
	wantRefusal(t, err, domain.ReasonNotActive)
	if got, _ := f.st.Get(t.Context(), gone.ID); got.State != domain.StateMissing {
		t.Errorf("on access: %s, want missing", got.State)
	}
	if err := f.svc.CheckAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]domain.State{gone.ID: domain.StateMissing, branchless.ID: domain.StateMissing, kept.ID: domain.StateActive} {
		if got, _ := f.st.Get(t.Context(), id); got.State != want {
			t.Errorf("%s: %s, want %s", id, got.State, want)
		}
	}
	if _, err := os.Stat(gone.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("a missing worktree's directory was recreated")
	}
	if out := f.git(f.repo, "branch", "--list", branchless.Branch); out != "" {
		t.Error("a missing worktree's branch was recreated")
	}
}

// TestWorktreeRefusesANonGitDirectory_REQ_WT_008: a cwd outside any repository is refused.
func TestWorktreeRefusesANonGitDirectory_REQ_WT_008(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Create(t.Context(), worktrees.CreateRequest{ThreadID: "thr_a", Cwd: t.TempDir()})
	wantRefusal(t, err, domain.ReasonNotGit)
}

// TestWorktreeRefusesABareRepo_REQ_WT_008: a bare repository is refused.
func TestWorktreeRefusesABareRepo_REQ_WT_008(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	f.git(f.repo, "clone", "-q", "--bare", f.repo, bare)
	_, err := f.svc.Create(t.Context(), worktrees.CreateRequest{ThreadID: "thr_a", Cwd: bare})
	wantRefusal(t, err, domain.ReasonBare)
}

// TestWorktreeRefusesAnOldGit_REQ_WT_008: a git older than 2.34 is refused before it is asked to
// do anything else.
func TestWorktreeRefusesAnOldGit_REQ_WT_008(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := t.TempDir()
	oldGit := fakeProgram(t, dir, "git", "git version 2.30.1", "", 0)
	svc := worktrees.New(worktrees.Options{
		Store: f.st, Git: gitcli.New(gitcli.Options{Bin: oldGit, Env: f.env}), Root: f.data,
		NewID: func() string { return "wtr_x" }, Now: time.Now,
	})
	_, err := svc.Create(t.Context(), worktrees.CreateRequest{ThreadID: "thr_a", Cwd: f.repo})
	wantRefusal(t, err, domain.ReasonOldGit)
	if rows, _ := f.st.ListActive(t.Context()); len(rows) != 0 {
		t.Error("a refused create stored a row")
	}
}

// TestARedirectedWorktreeGitDirIsNotFollowed_REQ_WT_004: the agent writes inside its worktree, so
// it can replace the worktree's `.git` file with one pointing at a git directory of its own whose
// configuration names a filter driver. Neither the diff nor the merge may run it: the engine pins
// the worktree's git directory from the main repository's own records (Art. 5).
func TestARedirectedWorktreeGitDirIsNotFollowed_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	marker := filepath.Join(t.TempDir(), "ran")
	evil := filepath.Join(t.TempDir(), "evil.git")
	f.git(f.repo, "clone", "-q", "--bare", f.repo, evil)
	f.git(evil, "config", "core.bare", "false")
	f.git(evil, "config", "filter.x.clean", "touch "+marker+"; cat")
	f.git(evil, "config", "core.fsmonitor", "touch "+marker+" #")
	f.write(w.Path, ".gitattributes", "* filter=x\n")
	f.write(w.Path, "a.txt", "agent's change\n")
	f.write(w.Path, ".git", "gitdir: "+evil+"\n")

	if _, err := f.svc.Diff(t.Context(), w.ID); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the diff ran a command the redirected git directory named")
	}
	if _, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the merge ran a command the redirected git directory named")
	}
	if f.read(f.repo, "a.txt") != "agent's change\n" {
		t.Error("the merge did not land the worktree's change")
	}
}

// TestAMergeNeverOverwritesAnIgnoredFile_REQ_WT_004: git's cherry-pick overwrites an untracked
// file the main copy ignores — the user's `.env` — when the worktree carries that path, and the
// rollback would then delete it. The merge is refused as a conflict naming the path before
// anything is applied, and both copies are left as they were.
func TestAMergeNeverOverwritesAnIgnoredFile_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(f.repo, ".gitignore", ".env\n")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", "ignore .env")
	f.write(f.repo, ".env", "the user's secret\n")
	w := f.create("thr_a")
	f.write(w.Path, ".env", "agent\n")
	f.git(w.Path, "add", "-f", ".env")
	f.git(w.Path, "commit", "-q", "-m", "carry .env")
	main := f.state(f.repo)

	_, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	r := wantRefusal(t, err, domain.ReasonConflict)
	if len(r.Paths) != 1 || r.Paths[0] != ".env" {
		t.Errorf("paths %q, want [.env]", r.Paths)
	}
	if got := f.read(f.repo, ".env"); got != "the user's secret\n" {
		t.Errorf("the main copy's .env is %q", got)
	}
	if f.state(f.repo) != main {
		t.Error("the main copy was changed")
	}
}

// TestASlowPostCommitHookStillLands_REQ_WT_004: the commit timeout can pass while a post-commit
// hook runs, after HEAD has moved. The work has landed then, so the merge answers with the
// commit and marks the worktree merged instead of reporting commit_failed over landed work.
func TestASlowPostCommitHookStillLands_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "worktree\n")
	f.write(f.repo, ".git/hooks/post-commit", "#!/bin/sh\nsleep 30\n")
	if err := os.Chmod(filepath.Join(f.repo, ".git/hooks/post-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if head := f.git(f.repo, "rev-parse", "HEAD"); res.Commit != head {
		t.Errorf("commit %q, HEAD %q", res.Commit, head)
	}
	if f.read(f.repo, "a.txt") != "worktree\n" {
		t.Error("the change is not in the main copy")
	}
	if got, _ := f.st.Get(t.Context(), w.ID); got.State != domain.StateMerged {
		t.Errorf("state %s, want merged", got.State)
	}
}

// TestAMergeOfWorkAlreadyOnTheBranchIsEmpty_REQ_WT_004: when the origin branch already holds
// exactly the worktree's change, the merge makes no commit and is not a failure.
func TestAMergeOfWorkAlreadyOnTheBranchIsEmpty_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "same\n")
	f.write(f.repo, "a.txt", "same\n")
	f.git(f.repo, "commit", "-q", "-am", "same change")
	head := f.git(f.repo, "rev-parse", "HEAD")

	res, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Commit != "" || f.git(f.repo, "rev-parse", "HEAD") != head {
		t.Errorf("commit %q; HEAD moved", res.Commit)
	}
	if got, _ := f.st.Get(t.Context(), w.ID); got.State != domain.StateMerged {
		t.Errorf("state %s, want merged", got.State)
	}
}

// TestAFailedCreateLeavesNothing_REQ_WT_001: a `worktree add` that fails part way — here a
// post-checkout hook — leaves no directory and no branch behind, so a retry can succeed.
func TestAFailedCreateLeavesNothing_REQ_WT_001(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hook := filepath.Join(f.repo, ".git/hooks/post-checkout")
	f.write(f.repo, ".git/hooks/post-checkout", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(t.Context(), worktrees.CreateRequest{ThreadID: "thr_a", Cwd: f.repo}); err == nil {
		t.Fatal("Create succeeded past a failing post-checkout hook")
	}
	if out := f.git(f.repo, "branch", "--list", "umbral/thr_a"); out != "" {
		t.Errorf("branch left behind: %q", out)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	f.create("thr_a")
}

// TestWorktreeDiffReadsPathsWhateverTheConfig_REQ_WT_003: `diff.noprefix` in the user's
// configuration and a path git quotes do not change the paths the diff reports.
func TestWorktreeDiffReadsPathsWhateverTheConfig_REQ_WT_003(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.repo, "config", "diff.noprefix", "true")
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "changed\n")
	f.write(w.Path, `we"ird b.txt`, "new\n")
	diff, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	paths := make([]string, 0, len(diff.Files))
	for _, d := range diff.Files {
		paths = append(paths, d.Path)
	}
	if strings.Join(paths, "|") != `a.txt|we"ird b.txt` {
		t.Errorf("paths %q", paths)
	}
}

// TestShowingARedirectedWorktreeRunsNoFilter_REQ_WT_003: the filter drivers turned off while a
// worktree is shown are read from the configuration the command uses — the main repository's,
// through the pinned git directory — not from whatever the worktree's own `.git` names.
func TestShowingARedirectedWorktreeRunsNoFilter_REQ_WT_003(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	f.git(f.repo, "config", "filter.x.clean", "touch "+marker+"; cat")
	w := f.create("thr_a")
	empty := filepath.Join(t.TempDir(), "empty.git")
	f.git(f.repo, "init", "-q", "--bare", empty)
	f.write(w.Path, ".gitattributes", "* filter=x\n")
	f.write(w.Path, "a.txt", "agent's change\n")
	f.write(w.Path, ".git", "gitdir: "+empty+"\n")
	if _, err := f.svc.Diff(t.Context(), w.ID); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("showing the worktree ran the main repository's filter driver")
	}
}

// TestAMergeRefusesAWorktreeChangedSinceItsReview_REQ_WT_004: a merge that names the revision
// of the diff it reviewed is refused `changed`, landing nothing, when the worktree no longer
// holds that revision; the same revision merges.
func TestAMergeRefusesAWorktreeChangedSinceItsReview_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "reviewed\n")
	reviewed, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	f.write(w.Path, "a.txt", "changed after the review\n")
	main := f.state(f.repo)
	_, err = f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Revision: reviewed.Revision})
	wantRefusal(t, err, domain.ReasonChanged)
	if f.state(f.repo) != main {
		t.Error("the main copy was changed")
	}
	again, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if again.Revision == reviewed.Revision {
		t.Fatal("two different worktree contents have one revision")
	}
	if _, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Revision: again.Revision}); err != nil {
		t.Fatalf("Merge of the reviewed revision: %v", err)
	}
	if f.read(f.repo, "a.txt") != "changed after the review\n" {
		t.Error("the reviewed revision did not land")
	}
}

// TestAReviewedRevisionMergesThroughTheUsersFilter_REQ_WT_004: a clean filter makes the tree that
// lands differ from the tree shown, which is read with filters off. The reviewed revision still
// merges, landing the filtered content, and a change after the review is still refused.
func TestAReviewedRevisionMergesThroughTheUsersFilter_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.repo, "config", "filter.up.clean", "tr a-z A-Z")
	f.write(f.repo, ".gitattributes", "*.up filter=up\n")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", "filter")
	w := f.create("thr_a")
	f.write(w.Path, "x.up", "hello\n")
	reviewed, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	f.write(w.Path, "x.up", "other\n")
	_, err = f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Revision: reviewed.Revision})
	wantRefusal(t, err, domain.ReasonChanged)
	f.write(w.Path, "x.up", "hello\n")
	if _, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Revision: reviewed.Revision}); err != nil {
		t.Fatalf("Merge of the reviewed revision: %v", err)
	}
	if got := f.git(f.repo, "show", "HEAD:x.up"); got != "HELLO" {
		t.Errorf("landed %q, want the filtered HELLO", got)
	}
}

// TestAnIgnoredAttributesFileCannotReshapeAReviewedMerge_REQ_WT_004: a `.gitattributes` that
// .gitignore excludes is never in the reviewed tree, yet git reads it. Written after the review,
// it could route a reviewed file through one of the user's filters and land content the review
// never showed, so its presence refuses a merge whose landing tree differs from the reviewed one.
func TestAnIgnoredAttributesFileCannotReshapeAReviewedMerge_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git(f.repo, "config", "filter.up.clean", "tr a-z A-Z")
	f.write(f.repo, ".gitignore", "sub/.gitattributes\n")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", "ignore")
	w := f.create("thr_a")
	f.write(w.Path, "sub/y.txt", "hello\n")
	reviewed, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	f.write(w.Path, "sub/.gitattributes", "y.txt filter=up\n")
	_, err = f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Revision: reviewed.Revision})
	wantRefusal(t, err, domain.ReasonChanged)
}

// TestCloseGroupNeverDropsAnUnmergedChange_REQ_WT_007: closing a group reports every worktree of
// its threads with its state, and an active one as changed or clean; it discards only the active
// worktrees the request names, so a changed worktree nobody named keeps its work.
func TestCloseGroupNeverDropsAnUnmergedChange_REQ_WT_007(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	changed := f.create("thr_changed")
	f.write(changed.Path, "a.txt", "unmerged work\n")
	clean := f.create("thr_clean")
	named := f.create("thr_named")
	f.write(named.Path, "b.txt", "work the user chose to drop\n")
	merged := f.create("thr_merged")
	f.write(merged.Path, "c.txt", "landed\n")
	if _, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: merged.ID}); err != nil {
		t.Fatal(err)
	}
	outside := f.create("thr_outside")

	threads := []string{"thr_changed", "thr_clean", "thr_named", "thr_merged"}
	if _, err := f.svc.CloseGroup(t.Context(), domain.CloseGroupRequest{ThreadIDs: threads, Discard: []string{named.ID}}); err == nil {
		t.Fatal("a discard without confirm was accepted")
	} else {
		wantRefusal(t, err, domain.ReasonConfirmRequired)
	}
	_, err := f.svc.CloseGroup(t.Context(), domain.CloseGroupRequest{ThreadIDs: threads, Discard: []string{outside.ID}, Confirm: true})
	wantRefusal(t, err, domain.ReasonNotInGroup)
	if got, _ := f.st.Get(t.Context(), outside.ID); got.State != domain.StateActive {
		t.Fatal("a worktree outside the group was discarded")
	}

	entries, err := f.svc.CloseGroup(t.Context(), domain.CloseGroupRequest{ThreadIDs: threads, Discard: []string{named.ID}, Confirm: true})
	if err != nil {
		t.Fatalf("CloseGroup: %v", err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Worktree.ID] = fmt.Sprintf("%s/%s/%t", e.Worktree.State, e.Changes, e.Discarded)
	}
	want := map[string]string{
		changed.ID: "active/changed/false",
		clean.ID:   "active/clean/false",
		named.ID:   "discarded/changed/true",
		merged.ID:  "merged//false",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	if f.read(changed.Path, "a.txt") != "unmerged work\n" {
		t.Fatal("an unnamed worktree lost its change")
	}
	if _, err := os.Stat(named.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the named worktree is still on disk")
	}
}

// TestCloseGroupReportsAMissingWorktree_REQ_WT_007: a worktree removed behind Umbral's back is
// reported missing, and naming it for a discard is not an error: it has nothing to discard.
func TestCloseGroupReportsAMissingWorktree_REQ_WT_007(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gone := f.create("thr_gone")
	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	entries, err := f.svc.CloseGroup(t.Context(), domain.CloseGroupRequest{ThreadIDs: []string{"thr_gone"}, Discard: []string{gone.ID}, Confirm: true})
	if err != nil {
		t.Fatalf("CloseGroup: %v", err)
	}
	if len(entries) != 1 || entries[0].Worktree.State != domain.StateMissing || entries[0].Changes != "" || entries[0].Discarded {
		t.Fatalf("entries %+v, want one missing worktree, not discarded", entries)
	}
	if _, err := os.Stat(gone.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("a missing worktree was recreated")
	}
}

// TestCloseGroupDiscardsOnlyThroughItsGuard_REQ_WT_007: the guard — the agent runtime's "no turn
// is running" — decides each discard; a refused one discards nothing at all.
func TestCloseGroupDiscardsOnlyThroughItsGuard_REQ_WT_007(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.create("thr_a")
	b := f.create("thr_b")
	busy := errors.New("thr_b has a turn running")
	guard := func(_ context.Context, threadID string, fn func() error) error {
		if threadID == "thr_b" {
			return busy
		}
		return fn()
	}
	_, err := f.svc.CloseGroup(t.Context(), domain.CloseGroupRequest{
		ThreadIDs: []string{"thr_a", "thr_b"}, Discard: []string{a.ID, b.ID}, Confirm: true, Guard: guard,
	})
	if !errors.Is(err, busy) {
		t.Fatalf("err = %v, want the guard's", err)
	}
	for _, w := range []domain.Worktree{a, b} {
		if got, _ := f.st.Get(t.Context(), w.ID); got.State != domain.StateActive {
			t.Errorf("%s is %s: a refused group discarded something", w.ID, got.State)
		}
	}
}

// TestCloseGroupHoldsEveryGuardAcrossItsDiscards_REQ_WT_007: a turn that could start on one
// thread after another's worktree is gone would make a refused request that discarded
// something. Each guard is entered once and every discard runs inside all of them, so the
// guard of a thread whose turn starts later refuses before anything is discarded.
func TestCloseGroupHoldsEveryGuardAcrossItsDiscards_REQ_WT_007(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.create("thr_a")
	b := f.create("thr_b")
	busy := errors.New("thr_b has a turn running")
	calls := map[string]int{}
	guard := func(_ context.Context, threadID string, fn func() error) error {
		calls[threadID]++
		if threadID == "thr_b" && calls[threadID] > 1 {
			return busy // a turn started on thr_b after its first guard let go
		}
		return fn()
	}
	entries, err := f.svc.CloseGroup(t.Context(), domain.CloseGroupRequest{
		ThreadIDs: []string{"thr_a", "thr_b"}, Discard: []string{a.ID, b.ID}, Confirm: true, Guard: guard,
	})
	if err != nil {
		t.Fatalf("CloseGroup = %v", err)
	}
	if calls["thr_a"] != 1 || calls["thr_b"] != 1 {
		t.Fatalf("guards entered %v, want each once", calls)
	}
	for _, e := range entries {
		if !e.Discarded {
			t.Errorf("%s was not discarded", e.Worktree.ID)
		}
	}
}

// stuckGit is git whose worktree removal fails, as on a directory a process still holds.
type stuckGit struct{ ports.Git }

func (stuckGit) RemoveWorktree(context.Context, string, string, string) error {
	return errors.New("cannot remove: device busy")
}

// TestAMergeThatLandedIsAMergeAfterAFailedCleanUp_REQ_WT_004: once the commit is on the origin
// branch the merge has happened. A worktree directory that cannot be removed afterwards is
// left over, not a failure: the caller gets the commit, and the row says merged.
func TestAMergeThatLandedIsAMergeAfterAFailedCleanUp_REQ_WT_004(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.create("thr_a")
	f.write(w.Path, "b.txt", "new\n")
	svc := worktrees.New(worktrees.Options{
		Store: f.st, Git: stuckGit{gitcli.New(gitcli.Options{Env: f.env})}, Root: f.data, CommitTimeout: 2 * time.Second,
		NewID: func() string { return "wtr_unused" }, Now: func() time.Time { return time.UnixMilli(2000) },
	})
	res, err := svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID})
	if err != nil {
		t.Fatalf("Merge = %v, want the landed commit", err)
	}
	if res.Commit == "" || res.Commit != f.git(f.repo, "rev-parse", "HEAD") || res.Leftover == nil {
		t.Fatalf("result %+v, want HEAD's commit and the left-over directory", res)
	}
	if got, _ := f.st.Get(t.Context(), w.ID); got.State != domain.StateMerged {
		t.Fatalf("the row is %s, want merged", got.State)
	}
}

// TestListMarksAMissingWorktree_REQ_WT_006: listing is an access, so a worktree gone from disk is
// listed — and stored — as missing; ActiveFor no longer finds it.
func TestListMarksAMissingWorktree_REQ_WT_006(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gone := f.create("thr_gone")
	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	rows, err := f.svc.List(t.Context(), nil)
	if err != nil || len(rows) != 1 || rows[0].State != domain.StateMissing {
		t.Fatalf("List = %+v, %v; want one missing row", rows, err)
	}
	if _, ok, err := f.svc.ActiveFor(t.Context(), "thr_gone"); ok || err != nil {
		t.Fatalf("ActiveFor = %t, %v; want none", ok, err)
	}
}
