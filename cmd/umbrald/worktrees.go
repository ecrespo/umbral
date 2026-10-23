package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/worktrees"
	"github.com/ecrespo/umbral/internal/worktrees/adapters/gitcli"
	"github.com/ecrespo/umbral/internal/worktrees/adapters/sqlstore"
	wtdomain "github.com/ecrespo/umbral/internal/worktrees/domain"
)

// newWorktrees builds the worktree engine (T-F2-02) over the daemon's database and the git
// binary. Worktrees live in `worktrees/` beside the database — $XDG_DATA_HOME/umbral/worktrees
// for the default one (REQ-WT-001) — as the skill store does.
//
// Every active worktree gone from disk is marked missing before anyone can ask for it
// (REQ-WT-006). A git that cannot be run is logged and does not stop the daemon: worktrees
// are refused then, and everything else works.
func newWorktrees(ctx context.Context, db *store.Store, commitTimeout time.Duration, logger *slog.Logger) *worktrees.Service {
	svc := worktrees.New(worktrees.Options{
		Store:         sqlstore.New(db.DB()),
		Git:           gitcli.New(gitcli.Options{}),
		Root:          filepath.Join(filepath.Dir(db.Path()), "worktrees"),
		CommitTimeout: commitTimeout,
		NewID:         func() string { return store.NewID(store.PrefixWorktree) },
	})
	if err := svc.CheckAll(ctx); err != nil {
		logger.Warn("cannot check the worktrees at start", slog.Any("error", err))
	}
	return svc
}

// runtimeWorktrees is the engine as the agent runtime reaches it (agents ports.Worktrees).
type runtimeWorktrees struct{ svc *worktrees.Service }

func (w runtimeWorktrees) Create(ctx context.Context, threadID, cwd, base string) (agentsports.Worktree, error) {
	row, err := w.svc.Create(ctx, worktrees.CreateRequest{ThreadID: threadID, Cwd: cwd, Base: base})
	var refusal *wtdomain.Refusal
	if errors.As(err, &refusal) && !refusal.Conflict() {
		// Not a repository, bare, too old: the runtime keeps the thread without one (REQ-WT-008).
		return agentsports.Worktree{}, fmt.Errorf("%w: %w", agentsports.ErrWorktreeRefused, err)
	}
	if err != nil {
		return agentsports.Worktree{}, err
	}
	return agentsports.Worktree{ID: row.ID, Path: row.Path, RepoRoot: row.RepoRoot}, nil
}

func (w runtimeWorktrees) Active(ctx context.Context, threadID string) (agentsports.Worktree, bool, error) {
	row, ok, err := w.svc.ActiveFor(ctx, threadID)
	if err != nil || !ok {
		return agentsports.Worktree{}, false, err
	}
	return agentsports.Worktree{ID: row.ID, Path: row.Path, RepoRoot: row.RepoRoot}, true, nil
}
