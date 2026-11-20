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

// Confined satisfies sessions' port: the thread is Umbral's own, its next turn decides under
// `full-auto`, cwd is inside its worktree, and the sandbox enforces. Where the thread's record
// cannot be read the answer is yes, and Confine then refuses: a shell is not started
// unconfined because a question about it failed.
func (c *shellConfiner) Confined(ctx context.Context, threadID, cwd string) bool {
	b := c.bound.Load()
	if b == nil || !b.sandbox.Status().Enforcing() {
		return false
	}
	_, err := b.worktree(ctx, threadID, cwd)
	return !errors.Is(err, errNotFullAuto)
}

// errNotFullAuto is a thread whose shell is not to be confined.
var errNotFullAuto = errors.New("the thread is not in full-auto in its worktree")

// worktree is the worktree a `full-auto` thread's shell is confined to.
func (b *shellSandbox) worktree(ctx context.Context, threadID, cwd string) (string, error) {
	t, err := b.threads.Thread(ctx, threadID)
	if errors.Is(err, agentsdomain.ErrNotFound) {
		return "", errNotFullAuto
	}
	if err != nil {
		return "", err
	}
	if t.Agent != "" {
		// A hosted agent's terminals are its own process's children, confined with it.
		return "", errNotFullAuto
	}
	place, err := agents.TurnPlace(ctx, b.worktrees, isRepo, t)
	if err != nil {
		return "", err
	}
	root := resolved(place.WriteRoot)
	if place.Mode != secdomain.ModeFullAuto || place.WriteRoot == "" || !sandboxdomain.Within(resolved(cwd), root) {
		return "", errNotFullAuto
	}
	return root, nil
}

// Confine satisfies sessions' port: argv launched under the thread's profile. The shell
// writes its worktree and a directory of its own for temporary files, and reads everything
// but Umbral's directories — save read, its own start-up files.
func (c *shellConfiner) Confine(ctx context.Context, threadID, cwd string, read, argv []string) ([]string, []string, error) {
	b := c.bound.Load()
	if b == nil {
		return nil, nil, fmt.Errorf("%w: there is no sandbox", sandboxdomain.ErrProfile)
	}
	root, err := b.worktree(ctx, threadID, cwd)
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
