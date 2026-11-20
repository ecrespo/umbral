package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/ecrespo/umbral/internal/agents"
	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	sandboxdomain "github.com/ecrespo/umbral/internal/sandbox/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// shellConfiner confines an Umbral thread's own shell while the thread is in `full-auto`
// (T-HRD-17, REQ-SBX-006): what the policy lets run there unasked is then bounded by the
// kernel to the thread's worktree, and not by the policy's reading of a command line.
//
// The sessions service is built before the sandbox, the thread store and the worktree engine,
// so it is handed this value empty and bind fills it in. Until then no shell is confined,
// and no thread exists to be in `full-auto`.
type shellConfiner struct{ bound atomic.Pointer[shellSandbox] }

var _ sessports.ShellConfiner = (*shellConfiner)(nil)

// shellSandbox is what confining a thread's shell needs.
type shellSandbox struct {
	acpConfiner
	// shells holds one directory per confined shell, `<runtime>/shells/<thread>`: its
	// temporary files, the only place outside the worktree it writes.
	shells  string
	threads interface {
		Thread(ctx context.Context, id string) (agentsdomain.Thread, error)
	}
	worktrees agentsports.Worktrees
}

func (c *shellConfiner) bind(s *shellSandbox) { c.bound.Store(s) }

// Enforces satisfies sessions' port.
func (c *shellConfiner) Enforces() bool {
	b := c.bound.Load()
	return b != nil && b.sandbox.Status().Enforcing()
}

// FullAuto satisfies sessions' port: the thread is Umbral's own and its next turn would decide
// under `full-auto`. Where the thread's record cannot be read the answer is yes, and Confine
// then refuses: a shell is not started unconfined because a question about it failed.
func (c *shellConfiner) FullAuto(ctx context.Context, threadID string) bool {
	b := c.bound.Load()
	if b == nil {
		return false
	}
	_, err := b.worktree(ctx, threadID)
	return !errors.Is(err, errNotFullAuto)
}

// errNotFullAuto is a thread whose record does not say `full-auto` in an active worktree.
var errNotFullAuto = errors.New("the thread is not in full-auto in an active worktree")

// worktree is the worktree a `full-auto` thread's shell is confined to. Where the shell
// starts is not asked: the turn's directory can be made to resolve outside the worktree by a
// command of the thread's own, and a shell released on that account would be the escape.
func (b *shellSandbox) worktree(ctx context.Context, threadID string) (string, error) {
	t, err := b.threads.Thread(ctx, threadID)
	if errors.Is(err, agentsdomain.ErrNotFound) {
		return "", errNotFullAuto
	}
	if err != nil {
		return "", err
	}
	if t.Agent != "" {
		// A hosted thread's turns are its agent's, decided call by call (REQ-ACP-003).
		return "", errNotFullAuto
	}
	place, err := agents.TurnPlace(ctx, b.worktrees, isRepo, t)
	if err != nil {
		return "", err
	}
	if place.Mode != secdomain.ModeFullAuto || place.WriteRoot == "" {
		return "", errNotFullAuto
	}
	return resolved(place.WriteRoot), nil
}

// Confine satisfies sessions' port: argv launched under the thread's profile. The shell
// writes its worktree and a directory of its own for temporary files, and reads everything
// but Umbral's directories — save read, its own start-up files.
func (c *shellConfiner) Confine(ctx context.Context, threadID string, read, argv []string) ([]string, []string, error) {
	b := c.bound.Load()
	if b == nil {
		return nil, nil, fmt.Errorf("%w: there is no sandbox", sandboxdomain.ErrProfile)
	}
	// A turn that decided under `full-auto` on a record that has changed since has no
	// worktree to be confined to: its command fails rather than run unconfined.
	root, err := b.worktree(ctx, threadID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", sandboxdomain.ErrProfile, err)
	}
	if threadID == "" || filepath.Base(threadID) != threadID {
		return nil, nil, fmt.Errorf("%w: %q is not a thread id", sandboxdomain.ErrProfile, threadID)
	}
	// Emptied at each launch: what the last shell left there is not this one's.
	own := filepath.Join(b.shells, threadID)
	if err := os.RemoveAll(own); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", sandboxdomain.ErrProfile, err)
	}
	tmp := filepath.Join(own, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", sandboxdomain.ErrProfile, err)
	}
	// The directory just made, inside the runtime directory as the daemon resolved it at
	// start, and no link put in its place or above it by something an earlier shell of this
	// thread left running: the profile's rule goes where this path resolves.
	if info, err := os.Lstat(own); err != nil || !info.IsDir() || resolved(own) != filepath.Join(filepath.Dir(b.homes), "shells", threadID) {
		return nil, nil, fmt.Errorf("%w: %s is not the directory the daemon made", sandboxdomain.ErrProfile, own)
	}
	agent := sandboxdomain.Agent{
		WorkDir: root, Root: resolved(own), Extra: b.extra, Protected: b.protected,
		OwnWorktree: filepath.Dir(filepath.Dir(root)) == b.acpConfiner.worktrees && filepath.Base(root) == threadID,
	}
	for _, r := range read {
		agent.Read = append(agent.Read, resolved(r))
	}
	profile, err := sandboxdomain.AgentProfile(agent)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range profile.Write {
		if b.helper != "" && sandboxdomain.Within(b.helper, w) {
			return nil, nil, fmt.Errorf("%w: the daemon's executable %s is inside %s, which the shell may write", sandboxdomain.ErrProfile, b.helper, w)
		}
	}
	confined, err := b.sandbox.Command(profile, argv)
	if err != nil {
		return nil, nil, err
	}
	// zsh makes its temporary files from TMPPREFIX and does not read TMPDIR
	// (docs/spikes/2026-11-landlock.md).
	tmp = resolved(tmp)
	return confined, []string{"TMPDIR=" + tmp, "TMPPREFIX=" + filepath.Join(tmp, "zsh")}, nil
}
