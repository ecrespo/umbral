package sessions

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// maxTerminalOutput is how much of a running terminal's output is kept: its last MiB, which is
// what `terminal/output` can return at most (delta `2026-10-acp-mediation`, decision 8).
const maxTerminalOutput = 1 << 20

// Terminal is one ACP terminal (REQ-ACP-005): a session of its own that the thread owns, with
// shell integration and no pane, whose one command is typed as the agent and recorded as a
// block with origin agent, the thread's id and via acp. It is not the thread's PTY, which
// RunForThread keeps using.
type Terminal struct {
	s         *Service
	live      *liveSession
	run       *agentRun
	sessionID string
	// kill cancels the run, which stops the command's process group and keeps the shell.
	kill context.CancelFunc
	done chan struct{}

	mu sync.Mutex
	// plain strips the output as it arrives; text keeps the tail of its committed lines.
	plain  domain.PlainText
	text   []byte
	result *domain.AgentRun
	err    error
}

// StartTerminal opens the thread a new session in cwd and types command into it once its shell
// is ready. It returns when the command has been handed to the shell; Wait reports how it ended.
func (s *Service) StartTerminal(ctx context.Context, threadID, cwd, command string) (*Terminal, error) {
	if err := domain.ValidateAgentCommand(command); err != nil {
		return nil, err
	}
	if !validThread(threadID) {
		return nil, fmt.Errorf("%w: %q is not a thread id", domain.ErrValidation, threadID)
	}
	shell, err := terminalShell(exec.LookPath)
	if err != nil {
		return nil, err
	}
	// Held while the session is created and marked, so threadSession never sees it unmarked
	// and takes it for the thread's PTY.
	s.threadMu.Lock()
	session, err := s.Create(ctx, domain.CreateParams{
		Shell: shell, CWD: cwd, Size: domain.Size{Cols: 120, Rows: 40}, ShellIntegration: true, OwnerThreadID: threadID,
	})
	if err != nil {
		s.threadMu.Unlock()
		return nil, err
	}
	live := s.lookup(session.ID)
	if live == nil {
		s.threadMu.Unlock()
		return nil, fmt.Errorf("%w: %s", domain.ErrNotFound, session.ID)
	}
	live.mu.Lock()
	live.terminal = true
	live.mu.Unlock()
	s.threadMu.Unlock()

	if err := s.awaitIntegration(ctx, live); err != nil {
		_ = s.Close(context.WithoutCancel(ctx), session.ID)
		return nil, err
	}
	runCtx, kill := context.WithCancel(context.WithoutCancel(ctx))
	t := &Terminal{s: s, live: live, sessionID: session.ID, kill: kill, done: make(chan struct{})}
	t.run = &agentRun{threadID: threadID, via: domain.ViaACP, out: t.append, typed: make(chan error, 1), done: make(chan domain.AgentRun, 1)}
	go func() {
		defer close(t.done)
		res, err := s.runAgent(runCtx, live, t.run, command)
		if err != nil && res.Block.ID != "" {
			// Killed, and its block closed with the exit the shell reported: that is the end.
			err = nil
		}
		t.mu.Lock()
		if err == nil {
			t.result = &res
		}
		t.err = err
		t.mu.Unlock()
	}()
	// The terminal exists once its line is written: a claim or a write that failed is the
	// request's error, never a terminal that started (delta `2026-10-acp-mediation`, decision 7).
	select {
	case err := <-t.run.typed:
		if err == nil {
			return t, nil
		}
		_ = t.Release(context.WithoutCancel(ctx))
		return nil, err
	case <-ctx.Done():
		_ = t.Release(context.WithoutCancel(ctx))
		return nil, ctx.Err()
	}
}

// errNoPOSIXShell refuses a terminal on a machine with neither bash nor zsh.
var errNoPOSIXShell = fmt.Errorf("%w: an ACP terminal needs bash or zsh installed", domain.ErrValidation)

// terminalShell is bash, else zsh, found with look. The command line is quoted for a POSIX shell,
// so it never runs in fish, whose single quotes read `\'` as an escape; bash comes first because
// zsh writes its PROMPT_SP mark — a `%`, a line of spaces and a carriage return — inside the
// block, erasing a last line printed without a newline.
func terminalShell(look func(string) (string, error)) (string, error) {
	for _, name := range []string{"bash", "zsh"} {
		if p, err := look(name); err == nil {
			return p, nil
		}
	}
	return "", errNoPOSIXShell
}

// append keeps the tail of the command's output as plain text.
func (t *Terminal) append(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.plain.Write(b)
	t.text = append(t.text, t.plain.TakeCommitted()...)
	if over := len(t.text) - maxTerminalOutput; over > 0 {
		t.text = append(t.text[:0], t.text[over:]...)
	}
}

// SessionID is the terminal's session.
func (t *Terminal) SessionID() string { return t.sessionID }

// Started reports whether the shell has started the command: its block is open.
func (t *Terminal) Started() bool {
	t.live.mu.RLock()
	defer t.live.mu.RUnlock()
	return t.run.blockID != ""
}

// Output is the plain text the command printed so far, its unfinished last line included, and
// how it ended, nil while it runs.
func (t *Terminal) Output() (string, *domain.AgentRun) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := string(t.text) + t.plain.Current()
	if t.result != nil {
		res := *t.result
		return out, &res
	}
	return out, nil
}

// Wait waits for the command to end and returns its block, closed.
func (t *Terminal) Wait(ctx context.Context) (domain.AgentRun, error) {
	select {
	case <-t.done:
	case <-ctx.Done():
		return domain.AgentRun{}, ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.result == nil {
		return domain.AgentRun{}, t.err
	}
	return *t.result, nil
}

// Kill stops the command's process group, as a cancelled run_command is stopped, and keeps
// the shell.
func (t *Terminal) Kill() { t.kill() }

// Release kills a command still running and closes the terminal's session.
func (t *Terminal) Release(ctx context.Context) error {
	t.kill()
	if err := t.s.Close(ctx, t.sessionID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return nil
}
