// Package worktrees is the worktree engine (PRD F2 REQ-WT-001…008): a thread's isolated copy of
// a repository, created outside it, shown as a diff, squashed back into the branch it came from
// or discarded, and marked missing when someone removes it behind Umbral's back.
//
// The engine owns the rows' states and the order of the git operations; the git mechanics are
// the Git port's, and every refusal leaves both copies as they were.
package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/worktrees/domain"
	"github.com/ecrespo/umbral/internal/worktrees/ports"
)

// DefaultCommitTimeout is `[worktrees] commit_timeout`'s default (REQ-WT-004).
const DefaultCommitTimeout = 60 * time.Second

// Options wires the engine. cmd/umbrald fills it.
type Options struct {
	Store         ports.Store
	Git           ports.Git
	Root          string        // $XDG_DATA_HOME/umbral/worktrees
	CommitTimeout time.Duration // DefaultCommitTimeout when zero
	NewID         func() string // a wtr_ ULID
	Now           func() time.Time
}

// Service is the engine.
type Service struct {
	o Options
	// mu serialises the operations that change a repository — create, merge and discard — so
	// two threads landing into one working copy never interleave a cherry-pick and a reset.
	mu sync.Mutex

	// versionMu guards the version check's result. Only a definitive answer is kept: a git that
	// could not be run — a cancelled request, a missing binary — is asked again next time.
	versionMu   sync.Mutex
	versionDone bool
	versionErr  error
}

// New returns an engine.
func New(o Options) *Service {
	if o.CommitTimeout <= 0 {
		o.CommitTimeout = DefaultCommitTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// checkVersion refuses a git older than 2.34 once per daemon, before any other git runs.
func (s *Service) checkVersion(ctx context.Context) error {
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	if s.versionDone {
		return s.versionErr
	}
	out, err := s.o.Git.Version(ctx)
	if err != nil {
		return fmt.Errorf("worktrees: run git: %w", err)
	}
	if !domain.GitVersionSupported(out) {
		s.versionErr = &domain.Refusal{
			Reason: domain.ReasonOldGit,
			Detail: fmt.Sprintf("%s; %d.%d or later is required", out, domain.MinGitMajor, domain.MinGitMinor),
		}
	}
	s.versionDone = true
	return s.versionErr
}

// CreateRequest is `worktree.create`.
type CreateRequest struct {
	ThreadID string
	Cwd      string // the thread's cwd, inside the repository
	Base     string // a revision; HEAD when empty
}

// Create makes the thread's worktree (REQ-WT-001, REQ-WT-008).
func (s *Service) Create(ctx context.Context, req CreateRequest) (domain.Worktree, error) {
	if !domain.ValidThreadID(req.ThreadID) {
		return domain.Worktree{}, &domain.Refusal{Reason: domain.ReasonBadThread, Detail: req.ThreadID}
	}
	if err := s.checkVersion(ctx); err != nil {
		return domain.Worktree{}, err
	}
	repo, err := s.o.Git.Inspect(ctx, req.Cwd)
	if err != nil {
		return domain.Worktree{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok, err := s.o.Store.Active(ctx, req.ThreadID); err != nil {
		return domain.Worktree{}, err
	} else if ok {
		return domain.Worktree{}, &domain.Refusal{Reason: domain.ReasonExists, Detail: req.ThreadID}
	}
	base := repo.Head
	if req.Base != "" {
		if base, err = s.o.Git.ResolveCommit(ctx, repo.Top, req.Base); err != nil {
			return domain.Worktree{}, err
		}
	}
	now := s.o.Now().UnixMilli()
	w := domain.Worktree{
		ID: s.o.NewID(), ThreadID: req.ThreadID, RepoRoot: repo.Top, RepoID: domain.RepoID(repo.CommonDir),
		BaseCommit: base, OriginBranch: repo.Branch, Branch: domain.BranchFor(req.ThreadID),
		State: domain.StateActive, CreatedAt: now, UpdatedAt: now,
	}
	w.Path = filepath.Join(s.o.Root, w.RepoID, req.ThreadID)
	if _, err := os.Lstat(w.Path); err == nil {
		return domain.Worktree{}, fmt.Errorf("worktrees: %s already exists", w.Path)
	}
	if err := s.o.Git.AddWorktree(ctx, w.RepoRoot, w.Path, w.Branch, w.BaseCommit); err != nil {
		// git can fail part way — a post-checkout hook runs after the checkout — and leave the
		// directory and the branch, which would refuse every retry. The path did not exist above.
		return domain.Worktree{}, errors.Join(err, s.o.Git.RemoveWorktree(context.WithoutCancel(ctx), w.RepoRoot, w.Path, w.Branch))
	}
	if err := s.o.Store.Insert(ctx, w); err != nil {
		// No row may be missing for a worktree on disk: undo the git side.
		return domain.Worktree{}, errors.Join(err, s.o.Git.RemoveWorktree(context.WithoutCancel(ctx), w.RepoRoot, w.Path, w.Branch))
	}
	return w, nil
}

// Get returns a worktree, marking it missing first if it has gone (REQ-WT-006).
func (s *Service) Get(ctx context.Context, id string) (domain.Worktree, error) {
	w, err := s.o.Store.Get(ctx, id)
	if err != nil {
		return domain.Worktree{}, err
	}
	return s.check(ctx, w)
}

// check marks an active worktree missing when its directory or its branch is gone. It never
// recreates either, and touches no other branch.
func (s *Service) check(ctx context.Context, w domain.Worktree) (domain.Worktree, error) {
	if w.State != domain.StateActive {
		return w, nil
	}
	var present bool
	if info, err := os.Stat(w.Path); err != nil || !info.IsDir() {
		present = false
	} else if ok, err := s.o.Git.BranchExists(ctx, w.RepoRoot, w.Branch); err != nil {
		if _, statErr := os.Stat(w.RepoRoot); statErr != nil {
			present = false // the repository itself is gone
		} else {
			return w, err
		}
	} else {
		present = ok
	}
	if present {
		return w, nil
	}
	now := s.o.Now().UnixMilli()
	if err := s.o.Store.SetState(ctx, w.ID, domain.StateMissing, now); err != nil {
		return w, err
	}
	w.State, w.UpdatedAt = domain.StateMissing, now
	return w, nil
}

// CheckAll marks every active worktree that has gone as missing; the daemon runs it at start.
func (s *Service) CheckAll(ctx context.Context) error {
	rows, err := s.o.Store.ListActive(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, w := range rows {
		if _, err := s.check(ctx, w); err != nil {
			errs = append(errs, fmt.Errorf("worktree %s: %w", w.ID, err))
		}
	}
	return errors.Join(errs...)
}

// active returns the worktree if it is still active after the check, and a not_active refusal
// otherwise.
func (s *Service) active(ctx context.Context, id string) (domain.Worktree, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return w, err
	}
	if w.State != domain.StateActive {
		return w, &domain.Refusal{Reason: domain.ReasonNotActive, Detail: string(w.State)}
	}
	return w, nil
}

// DiffResult is a worktree's changes and the revision they were read from.
type DiffResult struct {
	Files []domain.FileDiff
	// Revision names the content shown. A merge that passes it lands exactly that content or
	// nothing (MergeRequest.Revision).
	Revision string
}

// Diff is the worktree's changes since its base, split into files and hunks (REQ-WT-003).
func (s *Service) Diff(ctx context.Context, id string) (DiffResult, error) {
	w, err := s.active(ctx, id)
	if err != nil {
		return DiffResult{}, err
	}
	text, revision, err := s.o.Git.Diff(ctx, w.RepoRoot, w.Path, w.BaseCommit)
	if err != nil {
		return DiffResult{}, err
	}
	return DiffResult{Files: domain.ParseUnifiedDiff(text), Revision: revision}, nil
}

// MergeRequest is `worktree.merge`.
type MergeRequest struct {
	ID      string
	Message string // the generated one when empty
	// Revision, when set, is the DiffResult.Revision the user reviewed: the merge is refused
	// `changed` if the worktree no longer holds it, so the agent cannot slip in work after the
	// review.
	Revision string
}

// MergeResult is what a merge made.
type MergeResult struct {
	Commit string // "" when the worktree changed nothing
}

// Merge squashes the worktree into its origin branch in the main copy, then removes the
// worktree's directory and branch and marks it merged (REQ-WT-004).
func (s *Service) Merge(ctx context.Context, req MergeRequest) (MergeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.active(ctx, req.ID)
	if err != nil {
		return MergeResult{}, err
	}
	repo, err := s.o.Git.Inspect(ctx, w.RepoRoot)
	if err != nil {
		return MergeResult{}, err
	}
	switch {
	case repo.Branch == "":
		return MergeResult{}, &domain.Refusal{Reason: domain.ReasonDetached, Detail: w.RepoRoot}
	case repo.Branch != w.OriginBranch:
		return MergeResult{}, &domain.Refusal{
			Reason: domain.ReasonOtherBranch,
			Detail: fmt.Sprintf("%s is on %s, the worktree came from %q", w.RepoRoot, repo.Branch, w.OriginBranch),
		}
	}
	if dirty, err := s.o.Git.Dirty(ctx, w.RepoRoot); err != nil {
		return MergeResult{}, err
	} else if dirty {
		return MergeResult{}, &domain.Refusal{Reason: domain.ReasonDirty, Detail: w.RepoRoot}
	}
	msg := req.Message
	if msg == "" {
		msg = fmt.Sprintf("Merge Umbral worktree %s\n\nThread %s, from %s.", w.Branch, w.ThreadID, short(w.BaseCommit))
	}
	commit, err := s.o.Git.Squash(ctx, w.RepoRoot, w.Path, w.BaseCommit, req.Revision, msg, s.o.CommitTimeout)
	if err != nil {
		return MergeResult{}, err
	}
	// The work is in the origin branch now. The row is marked first: a directory left behind
	// by a failed removal is harmless, a row still active over landed work is not.
	if err := s.o.Store.SetState(ctx, w.ID, domain.StateMerged, s.o.Now().UnixMilli()); err != nil {
		return MergeResult{Commit: commit}, err
	}
	return MergeResult{Commit: commit}, s.o.Git.RemoveWorktree(ctx, w.RepoRoot, w.Path, w.Branch)
}

// Discard removes the worktree's directory and branch and keeps its row as discarded. It needs
// confirm, since the work in it is lost (REQ-WT-005).
func (s *Service) Discard(ctx context.Context, id string, confirm bool) error {
	if !confirm {
		return &domain.Refusal{Reason: domain.ReasonConfirmRequired, Detail: "discarding a worktree loses its changes"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.active(ctx, id)
	if err != nil {
		return err
	}
	if err := s.o.Git.RemoveWorktree(ctx, w.RepoRoot, w.Path, w.Branch); err != nil {
		return err
	}
	return s.o.Store.SetState(ctx, w.ID, domain.StateDiscarded, s.o.Now().UnixMilli())
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
