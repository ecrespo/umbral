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
)

var (
	// ErrAgentBusy is a second command for a thread whose PTY is still running one.
	ErrAgentBusy = errors.New("sessions: the thread's PTY is running another command")
	// ErrNoIntegration is a thread shell that never spoke OSC 133, so no block could record
	// the command.
	ErrNoIntegration = errors.New("sessions: the thread's shell has no shell integration")
)

// RunForThread runs one command line in the thread's PTY and waits for it (REQ-AGT-003). The
// PTY is the live session the thread owns, created on first use in cwd with shell
// integration and the agent holding its input; later commands reuse it and run wherever its
// shell now is. The command is typed as the agent and recorded as a block with origin agent
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

	run := &agentRun{threadID: threadID, done: make(chan domain.AgentRun, 1)}
	if err := s.claimPTY(ctx, live, run); err != nil {
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

	if err := s.Input(ctx, sessionID, []byte(command+"\n"), domain.InputOwnerAgent); err != nil {
		release()
		return domain.AgentRun{}, err
	}

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
	cancelled := time.Now()
	budget := time.NewTimer(agentCancelBudget)
	defer budget.Stop()
	s.signalAgent(live, ports.SignalTermForeground)
	term := time.NewTimer(agentTermGrace)
	defer term.Stop()
	for {
		select {
		case result := <-run.done:
			return result, ctx.Err()
		case <-term.C:
			s.signalAgent(live, ports.SignalKillForeground)
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

func (s *Service) signalAgent(live *liveSession, sig ports.SignalKind) {
	if err := live.pty.Signal(sig); err != nil {
		s.cfg.Logger.Warn("could not stop the agent's command", "session_id", live.snapshotState().ID, "error", err)
	}
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
	for _, live := range s.live {
		live.mu.RLock()
		mine := live.session.OwnerThreadID == threadID && live.session.State == domain.StateAlive
		live.mu.RUnlock()
		if mine {
			s.mu.Unlock()
			return live, nil
		}
	}
	s.mu.Unlock()

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
	return live, nil
}

func validThread(id string) bool { return len(id) > 4 && id[:4] == "thr_" }

// awaitIntegration waits for the session's shell to prove its integration, which is when it
// can be typed into and its commands become blocks.
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
		case integration == domain.IntegrationNone:
			return ErrNoIntegration
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
