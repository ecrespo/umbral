package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// Bounds on the agent's use of a thread's PTY.
const (
	// agentReadyTimeout is how long a new thread shell has to show its first prompt. It is
	// generous for the reason the integration tests give: a loaded machine can take tens of
	// seconds to start a shell through its hooks.
	agentReadyTimeout = 60 * time.Second
	// agentTermGrace is how long a cancelled command has between SIGTERM and SIGKILL (Tech
	// §3 step 3), and agentCancelBudget the whole of REQ-AGT-007's 500 ms, less a margin for
	// the caller.
	agentTermGrace    = 300 * time.Millisecond
	agentCancelBudget = 450 * time.Millisecond
	// agentBusyWait is how long a run waits for a cancelled one to let go of the PTY.
	agentBusyWait = 2 * time.Second
	// agentInterruptEvery is how often a cancelled command's shell is interrupted again while
	// the command holds the PTY, and agentStopWindow how long that goes on.
	agentInterruptEvery = 100 * time.Millisecond
	agentStopWindow     = 5 * time.Second
)

var (
	// ErrAgentBusy is a second command for a thread whose PTY is still running one.
	ErrAgentBusy = errors.New("sessions: the thread's PTY is running another command")
	// ErrProgramRunning is a command for a thread's PTY whose foreground holds a program that
	// terminal_interact started, or the user did: it would be typed into that program.
	ErrProgramRunning = errors.New("sessions: a program is running in the thread's terminal; use terminal_interact to drive or end it")
	// ErrPromptHasText is a command for a thread's PTY whose prompt holds text terminal_interact
	// typed and did not enter: the two would run as one line.
	ErrPromptHasText = errors.New("sessions: the thread's terminal has text typed at its prompt; enter it or send ctrl+c with terminal_interact first")
	// ErrNoIntegration is a thread shell that did not speak OSC 133 within agentReadyTimeout,
	// so no block could record the command.
	ErrNoIntegration = errors.New("sessions: the thread's shell has no shell integration")
)

// RunForThread runs one command line in the thread's PTY and waits for it (REQ-AGT-003). The
// PTY is the live session the thread owns, created on first use in cwd with shell
// integration and the agent holding its input; later commands with the same cwd reuse it and
// run wherever its shell now is. A different cwd — the thread moved into its worktree, or back
// out (REQ-WT-002) — closes that shell and starts a new one in the new cwd. The command is typed as the agent and recorded as a block with origin agent
// and the thread's id, which comes back closed, with its output.
//
// Cancelling ctx kills the command's process group, not the shell, and returns within
// REQ-AGT-007's 500 ms with ctx's error; the block closes with the exit the shell reports.
func (s *Service) RunForThread(ctx context.Context, threadID, cwd, command string) (domain.AgentRun, error) {
	if err := domain.ValidateAgentCommand(command); err != nil {
		return domain.AgentRun{}, err
	}
	live, err := s.threadSession(ctx, threadID, cwd)
	if err != nil {
		return domain.AgentRun{}, err
	}
	if err := s.awaitIntegration(ctx, live); err != nil {
		return domain.AgentRun{}, err
	}
	return s.runAgent(ctx, live, &agentRun{threadID: threadID, via: domain.ViaShell, done: make(chan domain.AgentRun, 1)}, command)
}

// runAgent types command into live as the agent and waits for its block, as RunForThread
// describes; run says whose command it is and what it comes through.
func (s *Service) runAgent(ctx context.Context, live *liveSession, run *agentRun, command string) (domain.AgentRun, error) {
	typed := func(err error) {
		if run.typed != nil {
			run.typed <- err
		}
	}
	if err := s.claimPTY(ctx, live, run); err != nil {
		typed(err)
		return domain.AgentRun{}, err
	}
	sessionID := live.snapshotState().ID
	release := func() {
		live.mu.Lock()
		if live.agent == run {
			live.agent = nil
		}
		live.mu.Unlock()
	}

	// An interrupt that reached the shell after a cancelled command had ended stays pending,
	// and bash and zsh spend it on the next line they parse by dropping its first byte. An
	// empty line ahead of the command takes it: losing that byte runs nothing, and an empty
	// line opens no block, so the agent's claim still falls on the command.
	line := []byte(command + "\n")
	if live.interrupted.Swap(false) {
		line = append([]byte{'\n'}, line...)
	}
	// What the user left on the line before handing the terminal back is dropped first
	// (REQ-FTU-002).
	if takeDiscard(live) {
		line = append(append([]byte(nil), discardKeys...), line...)
	}
	if err := s.Input(ctx, sessionID, line, domain.InputOwnerAgent); err != nil {
		release()
		typed(err)
		return domain.AgentRun{}, err
	}
	typed(nil)

	select {
	case result := <-run.done:
		return result, nil
	case <-live.done:
		release()
		select {
		case result := <-run.done:
			return result, nil
		default:
			return domain.AgentRun{}, fmt.Errorf("%w: %s", domain.ErrExited, sessionID)
		}
	case <-ctx.Done():
	}

	// REQ-AGT-007 and Tech §3 step 3: SIGTERM, then SIGKILL 300 ms later, all inside 500 ms.
	// A command the shell has not started yet is signalled when its block opens instead.
	cancelled := time.Now()
	budget := time.NewTimer(agentCancelBudget)
	defer budget.Stop()
	live.mu.Lock()
	run.cancelled = true
	started := run.blockID != ""
	live.mu.Unlock()
	if started {
		s.stopAgent(live, run)
	}
	for {
		select {
		case result := <-run.done:
			return result, ctx.Err()
		case <-budget.C:
			// The block has not closed. The run stays on the PTY, abandoned, until it does or
			// the shell prompts again, so its block is never taken for the next run's.
			live.mu.Lock()
			if live.agent == run {
				run.abandoned = true
			}
			live.mu.Unlock()
			s.cfg.Logger.Warn("a cancelled agent command did not close its block in time",
				"session_id", sessionID, "after", time.Since(cancelled))
			return domain.AgentRun{}, ctx.Err()
		}
	}
}

// claimPTY makes run the session's current agent run. A cancelled run still holding the PTY
// is waited for briefly; a live one makes this ErrAgentBusy.
func (s *Service) claimPTY(ctx context.Context, live *liveSession, run *agentRun) error {
	deadline := time.Now().Add(agentBusyWait)
	for {
		live.mu.Lock()
		switch {
		case live.takenOver:
			live.mu.Unlock()
			return domain.ErrTakenOver
		case live.interactLine != "":
			live.mu.Unlock()
			return ErrPromptHasText
		case live.agent == nil && live.blockOpen, live.agent != nil && live.agent.interactive:
			live.mu.Unlock()
			return ErrProgramRunning
		case live.agent == nil:
			live.agent = run
			live.mu.Unlock()
			return nil
		case !live.agent.abandoned || time.Now().After(deadline):
			live.mu.Unlock()
			return ErrAgentBusy
		}
		live.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// stopAgent stops a cancelled run's command: SIGTERM to what the shell runs and SIGINT to the
// shell, then SIGKILL agentTermGrace later. A shell can lose an interrupt — bash runs its
// DEBUG trap, the integration's preexec, before every command of a loop of builtins, and one
// that lands inside the trap may end the trap and not the loop — so it is interrupted again
// every agentInterruptEvery while the run holds the PTY and the PTY has stayed silent since
// the last one. Silence is the tell: a loop of builtins writes nothing, while a shell that
// did stop writes its prompt at once, and interrupting that shell again could land inside
// its prompt hook.
func (s *Service) stopAgent(live *liveSession, run *agentRun) {
	var seen uint64
	s.signalRun(live, run, ports.SignalTermForeground, nil, &seen)
	go func() {
		kill := time.NewTimer(agentTermGrace)
		defer kill.Stop()
		again := time.NewTicker(s.cfg.AgentInterruptEvery)
		defer again.Stop()
		give := time.NewTimer(agentStopWindow)
		defer give.Stop()
		for {
			select {
			case <-live.done:
				return
			case <-give.C:
				return
			case <-kill.C:
				s.signalRun(live, run, ports.SignalKillForeground, nil, nil)
			case <-again.C:
				if !s.signalRun(live, run, ports.SignalTermForeground, &seen, &seen) {
					return
				}
			}
		}
	}()
}

// signalRun signals the PTY only while run still holds it, and reports whether it does. With
// quietSince, it signals only if no output has arrived since that sequence number; seen, if
// given, receives the sequence number current at the call. The read lock is held across the
// check and the signal so the next run cannot claim the PTY, and start a command the signal
// would reach, in between.
func (s *Service) signalRun(live *liveSession, run *agentRun, sig ports.SignalKind, quietSince, seen *uint64) bool {
	live.mu.RLock()
	if live.agent != run {
		live.mu.RUnlock()
		return false
	}
	seq := live.seq
	quiet := quietSince == nil || *quietSince == seq
	if seen != nil {
		*seen = seq
	}
	var err error
	if quiet {
		err = live.pty.Signal(sig)
		if sig == ports.SignalTermForeground {
			live.interrupted.Store(true)
		}
	}
	sessionID := live.session.ID
	live.mu.RUnlock()
	if err != nil {
		s.cfg.Logger.Warn("could not stop the agent's command", "session_id", sessionID, "error", err)
	}
	return true
}

// threadSession returns the thread's live PTY, creating it if it has none.
func (s *Service) threadSession(ctx context.Context, threadID, cwd string) (*liveSession, error) {
	if !validThread(threadID) {
		return nil, fmt.Errorf("%w: %q is not a thread id", domain.ErrValidation, threadID)
	}
	// One thread's first two commands must not each create a PTY.
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	s.mu.Lock()
	var stale *liveSession
	for _, live := range s.live {
		live.mu.RLock()
		mine := live.session.OwnerThreadID == threadID && live.session.State == domain.StateAlive && !live.terminal
		same := live.threadCwd == cwd
		live.mu.RUnlock()
		if mine && same {
			s.mu.Unlock()
			return live, nil
		}
		if mine {
			stale = live
		}
	}
	s.mu.Unlock()
	if stale != nil {
		stale.mu.RLock()
		held := stale.takenOver
		stale.mu.RUnlock()
		if held {
			// The user's terminal is never closed under them (REQ-FTU-002).
			return nil, domain.ErrTakenOver
		}
		if err := s.Close(ctx, stale.snapshotState().ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		select {
		case <-stale.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	session, err := s.Create(ctx, domain.CreateParams{
		CWD: cwd, Size: domain.Size{Cols: 120, Rows: 40}, ShellIntegration: true, OwnerThreadID: threadID,
	})
	if err != nil {
		return nil, err
	}
	live := s.lookup(session.ID)
	if live == nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrNotFound, session.ID)
	}
	live.mu.Lock()
	live.threadCwd = cwd
	live.mu.Unlock()
	return live, nil
}

func validThread(id string) bool { return len(id) > 4 && id[:4] == "thr_" }

// awaitIntegration waits for the session's shell to prove its integration, which is when it
// can be typed into and its commands become blocks.
//
// `none` is not an answer here. REQ-BLK-003 sets it when the shell has been silent for 5 s and
// promotes the session to `osc133` when its markers arrive later — "the window is a heuristic
// about silence, not a verdict about the shell". A thread shell is always started with the
// integration, so a slow one is waited for up to agentReadyTimeout, like a shell that has not
// spoken yet; failing on the provisional `none` made run_command fail for good on a loaded
// machine, where a shell's hooks can take longer than the window.
func (s *Service) awaitIntegration(ctx context.Context, live *liveSession) error {
	deadline := time.NewTimer(agentReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		live.mu.RLock()
		integration, state := live.session.Integration, live.session.State
		live.mu.RUnlock()
		switch {
		case state == domain.StateExited:
			return fmt.Errorf("%w: %s", domain.ErrExited, live.session.ID)
		case integration == domain.IntegrationOSC133:
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrNoIntegration
		case <-tick.C:
		}
	}
}

// nullable stores "" as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}
