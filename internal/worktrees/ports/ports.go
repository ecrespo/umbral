// Package ports declares what the worktree engine needs from the outside: a store for its rows
// and a git it drives (Tech Design §5.2, `worktrees`).
package ports

import (
	"context"
	"time"

	"github.com/ecrespo/umbral/internal/worktrees/domain"
)

// Store keeps worktree rows (Data Model §2.4g).
type Store interface {
	Insert(ctx context.Context, w domain.Worktree) error
	// Get returns domain.ErrNotFound for an id no row holds.
	Get(ctx context.Context, id string) (domain.Worktree, error)
	// Active returns the thread's active worktree, if it has one.
	Active(ctx context.Context, threadID string) (domain.Worktree, bool, error)
	ListActive(ctx context.Context) ([]domain.Worktree, error)
	SetState(ctx context.Context, id string, s domain.State, at int64) error
}

// Repo is what Inspect learns about a directory inside a working copy.
type Repo struct {
	Top       string // the working copy's top level, real path
	CommonDir string // the common git directory, real path
	Head      string // HEAD's commit
	Branch    string // HEAD's branch; "" when detached
}

// Git is the git operations the engine needs. Every method leaves the repository as it was
// when it returns an error, unless its comment says otherwise.
type Git interface {
	// Version is `git --version`'s output.
	Version(ctx context.Context) (string, error)
	// Inspect refuses a directory outside any repository (ReasonNotGit), a bare repository
	// (ReasonBare) and one without a commit (ReasonNoCommit) with a *domain.Refusal.
	Inspect(ctx context.Context, dir string) (Repo, error)
	// ResolveCommit resolves rev to a commit id in dir.
	ResolveCommit(ctx context.Context, dir, rev string) (string, error)
	// AddWorktree creates path on a new branch from base.
	AddWorktree(ctx context.Context, repoRoot, path, branch, base string) error
	// BranchExists reports whether refs/heads/<branch> exists.
	BranchExists(ctx context.Context, repoRoot, branch string) (bool, error)
	// Dirty reports whether dir has tracked changes, staged or not.
	Dirty(ctx context.Context, dir string) (bool, error)
	// Diff is the unified diff from base to the worktree's working tree, untracked files
	// included, read without running anything the repository's configuration names. The
	// worktree is read through the main repository's record of it, never its own `.git` file.
	// revision names the worktree's content as shown: the tree it was read as.
	Diff(ctx context.Context, repoRoot, worktree, base string) (text, revision string, err error)
	// Squash applies everything the worktree changed since base onto repoRoot's HEAD as one
	// commit with message, running the repository's hooks and signing, the commit bounded by
	// timeout. It returns the new commit, or "" when the worktree changed nothing. A conflict
	// or a failed commit is a *domain.Refusal, with repoRoot and the worktree left as they were.
	// A non-empty revision is a Diff revision: the content committed must be the content it
	// names, checked after the snapshot that is committed, or the squash is refused `changed`.
	Squash(ctx context.Context, repoRoot, worktree, base, revision, message string, timeout time.Duration) (string, error)
	// RemoveWorktree removes the worktree's directory and its branch; either may already be gone.
	RemoveWorktree(ctx context.Context, repoRoot, path, branch string) error
}
