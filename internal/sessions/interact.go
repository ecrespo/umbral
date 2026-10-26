package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// interactPoll is how often wait_for looks at the PTY. A wait reads the emulator, which is
// cheap, and 20 ms is well inside what an agent driving a program can tell apart.
const interactPoll = 20 * time.Millisecond

// promptWait bounds how long a send waits for a new shell to draw its first prompt.
const promptWait = 10 * time.Second

// threadPTY is the thread's PTY for cwd, if it has one, and whether the one it has is taken
// over — which a turn in another cwd must not replace.
func (s *Service) threadPTY(threadID, cwd string) (live *liveSession, heldElsewhere bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.live {
		l.mu.RLock()
		mine := l.session.OwnerThreadID == threadID && l.session.State == domain.StateAlive && !l.terminal
		same, held := l.threadCwd == cwd, l.takenOver
		l.mu.RUnlock()
		switch {
		case mine && same:
			return l, false
		case mine && held:
			heldElsewhere = true
		}
	}
	return nil, heldElsewhere
}

// interactState reads how keys would be taken now, under mu (REQ-FTU-003).
func interactState(live *liveSession) domain.InteractState {
	live.mu.RLock()
	defer live.mu.RUnlock()
	return domain.InteractState{
		AtPrompt: live.session.Integration == domain.IntegrationOSC133 && live.promptReady &&
			!live.blockOpen && live.agent == nil,
		Line:      live.interactLine,
		Program:   live.programLine,
		TakenOver: live.takenOver,
	}
}

// InteractState is how keys sent to the thread's PTY in cwd would be read, without starting
// one: a thread with no PTY there yet gets a new shell, at its prompt with an empty line
// (delta `2026-10-terminal-interact`, decision 4).
func (s *Service) InteractState(threadID, cwd string) domain.InteractState {
	live, held := s.threadPTY(threadID, cwd)
	switch {
	case live != nil:
		return interactState(live)
	case held:
		return domain.InteractState{TakenOver: true}
	default:
		return domain.InteractState{AtPrompt: true}
	}
}

// interactPTY is the thread's PTY for a terminal_interact call, started if it has none, with
// its shell settled: integration proven and either at a prompt or running something.
func (s *Service) interactPTY(ctx context.Context, threadID, cwd string) (*liveSession, error) {
	live, err := s.threadSession(ctx, threadID, cwd)
	if err != nil {
		return nil, err
	}
	if err := s.awaitIntegration(ctx, live); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(promptWait)
	for {
		live.mu.RLock()
		settled := live.promptReady || live.blockOpen || live.agent != nil || live.takenOver
		live.mu.RUnlock()
		if settled || time.Now().After(deadline) {
			return live, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-live.done:
			return nil, fmt.Errorf("%w: %s", domain.ErrExited, live.session.ID)
		case <-time.After(interactPoll):
		}
	}
}

// SendKeys writes keys to the thread's PTY as the agent (REQ-FTU-001). expect is the state the
// call was decided on; if the terminal is no longer in it, nothing is written (decision 5). A
// line entered at the prompt is claimed for the agent, so its block has origin agent and the
// thread's id; the program it starts keeps the PTY until its block closes.
func (s *Service) SendKeys(ctx context.Context, threadID, cwd string, keys []domain.Key, expect domain.InteractState) (domain.Plan, error) {
	if err := domain.ValidateKeys(keys); err != nil {
		return domain.Plan{}, err
	}
	live, err := s.interactPTY(ctx, threadID, cwd)
	if err != nil {
		return domain.Plan{}, err
	}

	live.inputMu.Lock()
	defer live.inputMu.Unlock()
	state := interactState(live)
	switch {
	case state.TakenOver:
		return domain.Plan{}, domain.ErrTakenOver
	case state != expect:
		return domain.Plan{}, domain.ErrTerminalChanged
	}
	plan, err := domain.PlanKeys(state, keys)
	if err != nil {
		return domain.Plan{}, err
	}
	data, err := encodeKeys(live, keys)
	if err != nil {
		return domain.Plan{}, err
	}

	var run *agentRun
	if state.AtPrompt {
		// An interrupt left pending eats the first byte of the next line the shell parses
		// (RunForThread); an empty line ahead of a fresh one takes it. It is written on its own
		// and its prompt awaited, so that prompt cannot withdraw the claim of the line after it.
		if state.Line == "" && takeDiscard(live) {
			if err := writeInput(live, discardKeys, domain.InputOwnerAgent); err != nil {
				return domain.Plan{}, err
			}
		}
		if state.Line == "" && live.interrupted.Swap(false) {
			if err := s.settleInterrupt(ctx, live); err != nil {
				return domain.Plan{}, err
			}
		}
		if plan.Submit && strings.TrimSpace(plan.Line) != "" {
			run = &agentRun{threadID: threadID, via: domain.ViaShell, interactive: true, done: make(chan domain.AgentRun, 1)}
		}
	}
	live.mu.Lock()
	if run != nil {
		live.agent = run
	}
	before := live.seq
	live.mu.Unlock()

	if err := writeInput(live, data, domain.InputOwnerAgent); err != nil {
		live.mu.Lock()
		if live.agent == run && run != nil {
			live.agent = nil
		}
		live.mu.Unlock()
		return domain.Plan{}, err
	}
	live.mu.Lock()
	switch {
	case !state.AtPrompt:
		live.programLine = plan.Program
	case plan.Submit:
		live.interactLine = ""
	default:
		live.interactLine = plan.Line
	}
	live.mu.Unlock()
	if plan.Interrupt {
		live.interrupted.Store(true)
	}
	// Keys at the prompt that enter nothing return once the shell has answered them. Text the
	// line editor has not read yet is still in the terminal's input queue, where an interrupt
	// that follows flushes it, so a ctrl+c sent next could leave part of the line behind; and
	// an interrupt is only acted on when the editor next looks, so a line typed after it could
	// be read first and enter what it dropped. The editor answers both by writing — the echo,
	// `^C` and the prompt again — so the call waits for output after the write to settle, or
	// settleWait.
	if state.AtPrompt && !plan.Submit {
		if err := s.awaitAnswer(ctx, live, before); err != nil {
			return domain.Plan{}, err
		}
	}
	return plan, nil
}

// takeDiscard reports, once, that a hand-back left the prompt's line to drop.
func takeDiscard(live *liveSession) bool {
	live.mu.Lock()
	defer live.mu.Unlock()
	due := live.discardLine
	live.discardLine = false
	return due
}

// settleWait bounds the wait for the prompt an empty line draws: when the pending interrupt ate
// its byte there is none.
const settleWait = 500 * time.Millisecond

// settleInterrupt types an empty line for a pending interrupt to spend, under inputMu, and waits
// for the prompt it draws.
func (s *Service) settleInterrupt(ctx context.Context, live *liveSession) error {
	live.mu.RLock()
	before := live.prompts
	live.mu.RUnlock()
	if err := writeInput(live, []byte{'\r'}, domain.InputOwnerAgent); err != nil {
		return err
	}
	_, err := s.awaitPrompt(ctx, live, before)
	return err
}

// answerQuiet is how long output must pause before an interrupt's answer counts as written.
const answerQuiet = 50 * time.Millisecond

// awaitAnswer waits up to settleWait for output after the before'th sequence number followed
// by answerQuiet without more; a shell that writes nothing costs settleWait.
func (s *Service) awaitAnswer(ctx context.Context, live *liveSession, before uint64) error {
	deadline := time.Now().Add(settleWait)
	last, quietFrom := before, time.Time{}
	for time.Now().Before(deadline) {
		live.mu.RLock()
		seq := live.seq
		live.mu.RUnlock()
		switch {
		case seq != last:
			last, quietFrom = seq, time.Now()
		case seq != before && time.Since(quietFrom) >= answerQuiet:
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-live.done:
			return fmt.Errorf("%w: %s", domain.ErrExited, live.session.ID)
		case <-time.After(interactPoll):
		}
	}
	return nil
}

// awaitPrompt waits up to settleWait for a prompt drawn after the before'th, and reports
// whether one was.
func (s *Service) awaitPrompt(ctx context.Context, live *liveSession, before uint64) (bool, error) {
	deadline := time.Now().Add(settleWait)
	for time.Now().Before(deadline) {
		live.mu.RLock()
		drawn := live.prompts > before && live.promptReady
		live.mu.RUnlock()
		if drawn {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-live.done:
			return false, fmt.Errorf("%w: %s", domain.ErrExited, live.session.ID)
		case <-time.After(interactPoll):
		}
	}
	return false, nil
}

// encodeKeys turns keys into what the PTY receives: a text as typed, a named key as the
// emulator encodes it in the terminal's modes.
func encodeKeys(live *liveSession, keys []domain.Key) ([]byte, error) {
	var out []byte
	for _, k := range keys {
		if k.Text != "" {
			out = append(out, k.Text...)
			continue
		}
		b, err := live.emu.EncodeKey(k)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// readPTY is the thread's PTY in cwd for a read: one that exists, never started or replaced,
// because a read is decided as ReadOnly and starting a shell runs what its startup files and
// prompt run (delta `2026-10-terminal-interact`, decision 3).
func (s *Service) readPTY(threadID, cwd string) (*liveSession, error) {
	live, held := s.threadPTY(threadID, cwd)
	switch {
	case live != nil && interactState(live).TakenOver, live == nil && held:
		return nil, domain.ErrTakenOver
	case live == nil:
		return nil, domain.ErrNoTerminal
	}
	return live, nil
}

// ReadScreen is the thread's PTY as read_screen shows it (REQ-FTU-001).
func (s *Service) ReadScreen(ctx context.Context, threadID, cwd string) (domain.Screen, error) {
	live, err := s.readPTY(threadID, cwd)
	if err != nil {
		return domain.Screen{}, err
	}
	return s.screen(ctx, live)
}

func (s *Service) screen(ctx context.Context, live *liveSession) (domain.Screen, error) {
	text, _, _, err := s.ScreenText(ctx, live.snapshotState().ID)
	if err != nil {
		return domain.Screen{}, err
	}
	x, y, err := live.emu.Cursor()
	if err != nil {
		return domain.Screen{}, err
	}
	live.mu.RLock()
	size := live.session.Size
	live.mu.RUnlock()
	return domain.Screen{
		Text: domain.TailText(text), CursorX: x, CursorY: y, Size: size,
		AtPrompt: interactState(live).AtPrompt,
	}, nil
}

// WaitFor waits on the thread's PTY for cond, and returns what ended the wait with the screen
// then (REQ-FTU-001). A timeout is an outcome, not an error.
func (s *Service) WaitFor(ctx context.Context, threadID, cwd string, cond domain.WaitCondition) (domain.WaitOutcome, error) {
	if err := cond.Validate(); err != nil {
		return domain.WaitOutcome{}, err
	}
	live, err := s.readPTY(threadID, cwd)
	if err != nil {
		return domain.WaitOutcome{}, err
	}
	deadline := time.NewTimer(cond.Timeout)
	defer deadline.Stop()
	tick := time.NewTicker(interactPoll)
	defer tick.Stop()

	seqNow := func() uint64 {
		live.mu.RLock()
		defer live.mu.RUnlock()
		return live.seq
	}
	lastSeq, quietSince := seqNow(), time.Now()
	checked := uint64(0)
	first := true
	end := func(reason string) (domain.WaitOutcome, error) {
		screen, err := s.screen(ctx, live)
		if err != nil {
			return domain.WaitOutcome{}, err
		}
		return domain.WaitOutcome{Reason: reason, Screen: screen}, nil
	}
	for {
		state := interactState(live)
		seq := seqNow()
		if seq != lastSeq {
			lastSeq, quietSince = seq, time.Now()
		}
		select {
		case <-live.done:
			return end(domain.WaitExited)
		default:
		}
		switch {
		case state.TakenOver:
			return domain.WaitOutcome{}, domain.ErrTakenOver
		case cond.Exit && state.AtPrompt:
			return end(domain.WaitExited)
		case cond.Idle != 0 && time.Since(quietSince) >= cond.Idle:
			return end(domain.WaitIdle)
		case cond.Regex != nil && (first || seq != checked):
			first, checked = false, seq
			text, _, _, err := s.ScreenText(ctx, live.snapshotState().ID)
			if err != nil {
				return domain.WaitOutcome{}, err
			}
			if cond.Regex.MatchString(text) {
				return end(domain.WaitMatched)
			}
		}
		select {
		case <-ctx.Done():
			return domain.WaitOutcome{}, ctx.Err()
		case <-deadline.C:
			return end(domain.WaitTimeout)
		case <-tick.C:
		}
	}
}

// TakeOver moves the thread's PTY's input to the human at once and returns its session
// (REQ-FTU-002). The agent's next call on it is refused taken_over. holder is the connection
// that took it, whose close hands it back (Release).
func (s *Service) TakeOver(ctx context.Context, threadID, holder string) (string, error) {
	live := s.anyThreadPTY(threadID)
	if live == nil {
		return "", fmt.Errorf("%w: thread %s has no terminal", domain.ErrNotFound, threadID)
	}
	live.inputMu.Lock()
	defer live.inputMu.Unlock()
	live.mu.Lock()
	live.takenOver, live.takenBy = true, holder
	live.mu.Unlock()
	if err := s.setInputOwner(ctx, live, domain.InputOwnerHuman); err != nil {
		return "", err
	}
	return live.snapshotState().ID, nil
}

// HandBack gives the thread's PTY's input back to the agent; a terminal not taken over is left
// as it is. It types nothing: whatever the user left on the prompt's line, now or once a
// program they left running ends, is dropped by the agent's next write at the prompt, which
// starts with discardKeys (decision 6).
func (s *Service) HandBack(ctx context.Context, threadID string) error {
	live := s.anyThreadPTY(threadID)
	if live == nil {
		return fmt.Errorf("%w: thread %s has no terminal", domain.ErrNotFound, threadID)
	}
	return s.handBack(ctx, live)
}

func (s *Service) handBack(ctx context.Context, live *liveSession) error {
	live.inputMu.Lock()
	defer live.inputMu.Unlock()
	live.mu.RLock()
	held := live.takenOver
	live.mu.RUnlock()
	if !held {
		return nil
	}
	if err := s.setInputOwner(ctx, live, domain.InputOwnerAgent); err != nil {
		return err
	}
	live.mu.Lock()
	live.takenOver, live.takenBy = false, ""
	live.interactLine, live.programLine = "", ""
	live.discardLine = true
	live.mu.Unlock()
	return nil
}

// Release hands back every thread's PTY holder took over: a client that goes away does not keep
// the agent out (REQ-FTU-002).
func (s *Service) Release(ctx context.Context, holder string) {
	s.mu.RLock()
	var held []*liveSession
	for _, l := range s.live {
		l.mu.RLock()
		if l.takenOver && l.takenBy == holder {
			held = append(held, l)
		}
		l.mu.RUnlock()
	}
	s.mu.RUnlock()
	for _, l := range held {
		if err := s.handBack(ctx, l); err != nil {
			s.cfg.Logger.Warn("handing back a terminal its holder left", "session", l.snapshotState().ID, "err", err)
		}
	}
}

// discardKeys are what the agent's first write at the prompt after a hand-back starts with:
// ctrl+e and ctrl+u, end of line and kill to its start in the line editors' default (emacs)
// bindings, so whatever the user left there is dropped and the shell receives the agent's line
// alone (decision 6).
var discardKeys = []byte{0x05, 0x15}

// anyThreadPTY is the thread's live PTY whatever its cwd, nil when it has none.
func (s *Service) anyThreadPTY(threadID string) *liveSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.live {
		l.mu.RLock()
		mine := l.session.OwnerThreadID == threadID && l.session.State == domain.StateAlive && !l.terminal
		l.mu.RUnlock()
		if mine {
			return l
		}
	}
	return nil
}
