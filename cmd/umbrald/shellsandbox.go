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

// shellConfiner confines the shells the daemon starts for a thread in `full-auto`: the shell of
// Umbral's own thread (T-HRD-17, REQ-SBX-006) and each terminal of a hosted agent's (T-HRD-21,
// REQ-SBX-007). What the policy lets run there unasked is then bounded by the kernel to the
// thread's worktree, and not by the policy's reading of a command line.
//
// The sessions service is built before the sandbox, the thread store and the worktree engine,
// so it is handed this value empty and bind fills it in. Until then no shell is confined,
// and no thread exists to be in `full-auto`.
type shellConfiner struct{ bound atomic.Pointer[shellSandbox] }

var _ sessports.ShellConfiner = (*shellConfiner)(nil)

// shellSandbox is what confining a thread's shell needs.
type shellSandbox struct {
	acpConfiner
	// shells holds one directory per thread, `<runtime>/shells/<thread>`: the temporary files
	// of its confined shell, the only place outside the worktree it writes. A hosted agent's
	// terminals each have a `term-<random>` directory inside it.
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

// FullAuto satisfies sessions' port: the thread's next turn would decide under `full-auto`,
// whether the thread is Umbral's own or a hosted agent's. Where the thread's record cannot be read the answer is yes, and Confine
// then refuses: a shell is not started unconfined because a question about it failed.
func (c *shellConfiner) FullAuto(ctx context.Context, threadID string) bool {
	b := c.bound.Load()
	if b == nil {
		return false
	}
	_, _, _, err := b.worktree(ctx, threadID)
	return !errors.Is(err, errNotFullAuto)
}

// errNotFullAuto is a thread whose record does not say `full-auto` in an active worktree.
var errNotFullAuto = errors.New("the thread is not in full-auto in an active worktree")

// worktree is the worktree a `full-auto` thread's shell is confined to. Where the shell
// starts is not asked: the turn's directory can be made to resolve outside the worktree by a
// command of the thread's own, and a shell released on that account would be the escape.
//
// hosted says the thread is a hosted agent's: what is confined then is each terminal the
// agent opens, several of which may live at once (REQ-SBX-007). noNet says that agent was
// launched with its network confined behind its relay: its terminals then get no network,
// or they would be the way out it was kept from, with no row of what they sent.
func (b *shellSandbox) worktree(ctx context.Context, threadID string) (root string, hosted, noNet bool, err error) {
	t, err := b.threads.Thread(ctx, threadID)
	if errors.Is(err, agentsdomain.ErrNotFound) {
		return "", false, false, errNotFullAuto
	}
	if err != nil {
		return "", false, false, err
	}
	place, err := agents.TurnPlace(ctx, b.worktrees, isRepo, t)
	if err != nil {
		return "", false, false, err
	}
	if place.Mode != secdomain.ModeFullAuto || place.WriteRoot == "" {
		return "", false, false, errNotFullAuto
	}
	hosted = t.Agent != ""
	return resolved(place.WriteRoot), hosted, hosted && t.NetworkProfile == agentsdomain.NetworkConfined, nil
}

// Confine satisfies sessions' port: argv launched under the thread's profile. The shell
// writes its worktree and a directory of its own for temporary files, and reads everything
// but Umbral's directories — save read, its own start-up files.
func (c *shellConfiner) Confine(ctx context.Context, threadID string, read, argv []string) ([]string, []string, func() error, error) {
	made := ""
	confined, env, err := c.confine(ctx, threadID, read, argv, &made)
	if made == "" {
		return confined, env, nil, err
	}
	// Removed with its terminal: an agent in `full-auto` opens terminals unasked, and what
	// each left would otherwise fill the runtime directory, the socket's own.
	release := func() error { return os.RemoveAll(made) }
	if err != nil {
		_ = release()
		return nil, nil, nil, err
	}
	return confined, env, release, nil
}

// confine is Confine; made is set to the directory made for this shell alone, if any.
func (c *shellConfiner) confine(ctx context.Context, threadID string, read, argv []string, made *string) ([]string, []string, error) {
	b := c.bound.Load()
	if b == nil {
		return nil, nil, fmt.Errorf("%w: there is no sandbox", sandboxdomain.ErrProfile)
	}
	// A turn that decided under `full-auto` on a record that has changed since has no
	// worktree to be confined to: its command fails rather than run unconfined.
	root, hosted, noNet, err := b.worktree(ctx, threadID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", sandboxdomain.ErrProfile, err)
	}
	if threadID == "" || filepath.Base(threadID) != threadID {
		return nil, nil, fmt.Errorf("%w: %q is not a thread id", sandboxdomain.ErrProfile, threadID)
	}
	fail := func(err error) ([]string, []string, error) {
		return nil, nil, fmt.Errorf("%w: %w", sandboxdomain.ErrProfile, err)
	}
	// Umbral's own thread has one shell, and its directory is emptied at each launch: what
	// the last shell left there is not this one's. A hosted agent's terminals live side by
	// side, so each gets a directory of its own under the thread's, which is left alone.
	own := filepath.Join(b.shells, threadID)
	want := filepath.Join(filepath.Dir(b.homes), "shells", threadID)
	if !hosted {
		if err := os.RemoveAll(own); err != nil {
			return fail(err)
		}
	}
	if err := os.MkdirAll(own, 0o700); err != nil {
		return fail(err)
	}
	if hosted {
		// A terminal of this thread cannot write its siblings' directories: the profile
		// below opens only the one made here.
		if own, err = os.MkdirTemp(own, "term-"); err != nil {
			return fail(err)
		}
		want = filepath.Join(want, filepath.Base(own))
		*made = own
	}
	tmp := filepath.Join(own, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return fail(err)
	}
	// The directory just made, inside the runtime directory as the daemon resolved it at
	// start, and no link put in its place or above it by something an earlier shell of this
	// thread left running: the profile's rule goes where this path resolves.
	if info, err := os.Lstat(own); err != nil || !info.IsDir() || resolved(own) != want {
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
	if noNet {
		// No port at all; refused, not run open, where the network cannot be confined.
		profile.Net = &sandboxdomain.Net{}
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
