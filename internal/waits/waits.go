// Package waits is the server-owned wait engine (DD-011): thread.wait, the wait inside
// thread.send, and block.wait_output. Waits are driven by bus events, pinned to the turn in
// progress when they start, and bounded by a deadline; a timeout reports what was last
// observed and never resends or retries anything (REQ-AUT-001…004).
//
// The bus is lossy under pressure, so a thread wait also re-reads the store on a slow
// backstop tick, and an output wait that sees a gap in the sequence numbers re-reads the
// screen. Neither is client polling: the client makes one call and gets one answer.
package waits

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/waits/domain"
	"github.com/ecrespo/umbral/internal/waits/ports"
)

// DefaultBackstop is how often a thread wait re-reads the store in case the bus dropped the
// event it was waiting for.
const DefaultBackstop = time.Second

// outputBuffer is an output wait's subscription buffer: output arrives in bursts.
const outputBuffer = 1024

// Config wires the service.
type Config struct {
	Bus      *bus.Bus
	Threads  ports.Threads
	Terminal ports.Terminal
	// Backstop overrides DefaultBackstop; tests shorten it.
	Backstop time.Duration
	Now      func() time.Time
}

// Service is the wait engine.
type Service struct {
	cfg Config
}

// New returns the service.
func New(cfg Config) (*Service, error) {
	if cfg.Bus == nil {
		return nil, errors.New("waits: a bus is required")
	}
	if cfg.Backstop <= 0 {
		cfg.Backstop = DefaultBackstop
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

var _ ports.Waits = (*Service)(nil)

// Thread starts a thread wait: it validates, then subscribes before anything is read, so an
// event published between the caller's pin and its wait is not lost.
func (s *Service) Thread(_ context.Context, threadID string, until []string, timeoutMS int64) (ports.ThreadWait, error) {
	targets, err := domain.ParseTargets(until)
	if err != nil {
		return nil, err
	}
	timeout, err := domain.ParseTimeout(timeoutMS)
	if err != nil {
		return nil, err
	}
	if s.cfg.Threads == nil {
		return nil, errors.New("waits: the agent runtime is not wired in")
	}
	return &threadWait{
		s: s, threadID: threadID, targets: targets, timeout: timeout, start: s.cfg.Now(),
		sub: s.cfg.Bus.Subscribe(agentsports.KindThreadTurnFinished, agentsports.KindApprovalRequested),
	}, nil
}

type threadWait struct {
	s        *Service
	threadID string
	targets  domain.Targets
	timeout  time.Duration
	start    time.Time
	sub      *bus.Subscription
	tracker  *domain.Tracker
	// first is the reading PinCurrent took, evaluated first by Wait.
	first *domain.Observed
}

func (w *threadWait) PinCurrent(ctx context.Context) error {
	st, err := w.s.cfg.Threads.Status(ctx, w.threadID)
	if err != nil {
		return err
	}
	o := domain.Observe(st.State, st.Attention, st.TurnID)
	w.first = &o
	w.tracker = domain.NewTracker(st.TurnID, w.targets)
	return nil
}

func (w *threadWait) Pin(turnID string) {
	w.tracker = domain.NewTracker(turnID, w.targets)
}

func (w *threadWait) Close() { w.sub.Close() }

func (w *threadWait) Wait(ctx context.Context) (ports.ThreadResult, error) {
	defer w.Close()
	if w.tracker == nil {
		return ports.ThreadResult{}, errors.New("waits: a thread wait was never pinned")
	}
	settled := func(state domain.State) (ports.ThreadResult, error) {
		return ports.ThreadResult{
			ThreadID: w.threadID, TurnID: w.tracker.Pinned(), State: state,
			WaitedMS: w.s.cfg.Now().Sub(w.start).Milliseconds(),
		}, nil
	}
	read := func() (domain.Observed, error) {
		st, err := w.s.cfg.Threads.Status(ctx, w.threadID)
		if err != nil {
			return domain.Observed{}, err
		}
		return domain.Observe(st.State, st.Attention, st.TurnID), nil
	}

	// settle feeds a reading to the tracker; a reading of a later turn sends the wait to the
	// runtime's memory of how the pinned turn ended.
	misses := 0
	settle := func(o domain.Observed) (domain.State, bool, error) {
		if state, ok := w.tracker.Observe(o); ok {
			return state, true, nil
		}
		if !w.tracker.Replaced() {
			return "", false, nil
		}
		end, known, err := w.s.cfg.Threads.TurnEnd(ctx, w.threadID, w.tracker.Pinned())
		if err != nil {
			return "", false, err
		}
		if known {
			state, ok := w.tracker.End(domain.State(end))
			return state, ok, nil
		}
		return "", false, nil
	}

	o := w.first
	if o == nil {
		got, err := read()
		if err != nil {
			return ports.ThreadResult{}, err
		}
		o = &got
	}
	if state, ok, err := settle(*o); err != nil {
		return ports.ThreadResult{}, err
	} else if ok {
		return settled(state)
	}

	deadline := time.NewTimer(w.timeout - w.s.cfg.Now().Sub(w.start))
	defer deadline.Stop()
	backstop := time.NewTicker(w.s.cfg.Backstop)
	defer backstop.Stop()
	for {
		var next domain.Observed
		select {
		case <-ctx.Done():
			return ports.ThreadResult{}, ctx.Err()
		case <-deadline.C:
			last := w.tracker.Last()
			if last == "" && w.tracker.Replaced() {
				last = domain.StateUnknown
			}
			return ports.ThreadResult{}, &domain.TimeoutError{LastState: string(last), Waited: w.s.cfg.Now().Sub(w.start)}
		case <-backstop.C:
			got, err := read()
			if err != nil {
				return ports.ThreadResult{}, err
			}
			state, ok, err := settle(got)
			if err != nil {
				return ports.ThreadResult{}, err
			}
			if ok {
				return settled(state)
			}
			// The pinned turn's end is published before or just after a later turn begins;
			// two ticks without it, from the bus or the runtime, means it is lost.
			if w.tracker.Replaced() {
				if misses++; misses >= 2 {
					return settled(domain.StateUnknown)
				}
			}
			continue
		case ev := <-w.sub.C():
			if e, ok := ev.(agentsports.TurnFinished); ok && e.ThreadID == w.threadID && e.TurnID == w.tracker.Pinned() && e.EndState != "" {
				if state, ok := w.tracker.End(domain.State(e.EndState)); ok {
					return settled(state)
				}
				continue
			}
			got, relevant, err := w.event(ev, read)
			if err != nil {
				return ports.ThreadResult{}, err
			}
			if !relevant {
				continue
			}
			next = got
		}
		state, ok, err := settle(next)
		if err != nil {
			return ports.ThreadResult{}, err
		}
		if ok {
			return settled(state)
		}
	}
}

// event turns a bus event into a reading. The pinned turn's end is taken from the event,
// which carries the state the turn's end wrote: by the time the store is read, a new turn
// may have replaced it. An approval is the pinned turn blocked as long as that turn is still
// the thread's latest, even when it has been answered before the store is read.
func (w *threadWait) event(ev bus.Event, read func() (domain.Observed, error)) (domain.Observed, bool, error) {
	switch e := ev.(type) {
	case agentsports.TurnFinished:
		if e.ThreadID != w.threadID {
			return domain.Observed{}, false, nil
		}
		o, err := read()
		return o, err == nil, err
	case agentsports.ApprovalRequested:
		if e.Approval.ThreadID != w.threadID {
			return domain.Observed{}, false, nil
		}
		o, err := read()
		if err != nil {
			return o, false, err
		}
		if o.TurnID == w.tracker.Pinned() {
			o.State, o.Running = domain.StateBlocked, true
		}
		return o, true, nil
	}
	return domain.Observed{}, false, nil
}

// Output starts block.wait_output. It subscribes before it reads the screen, and the screen
// comes with the sequence number it is current as of, so the output after it is followed
// without a gap and without reading anything twice.
func (s *Service) Output(ctx context.Context, p domain.OutputParams) (ports.OutputWait, error) {
	p, err := p.Validate()
	if err != nil {
		return nil, err
	}
	matcher, err := domain.NewMatcher(p.Regex)
	if err != nil {
		return nil, err
	}
	if s.cfg.Terminal == nil {
		return nil, errors.New("waits: the sessions module is not wired in")
	}
	timeout, _ := domain.ParseTimeout(p.TimeoutMS)
	w := &outputWait{s: s, matcher: matcher, timeout: timeout, start: s.cfg.Now(), sessionID: p.SessionID}

	var block sessdomain.Block
	if p.BlockID != "" {
		block, err = s.cfg.Terminal.Block(ctx, p.BlockID)
		if err != nil {
			return nil, err
		}
		w.sessionID, w.blockID, w.pinnedBlock = block.SessionID, block.ID, true
	}
	w.sub = s.cfg.Bus.SubscribeBuffered(outputBuffer,
		sessports.KindSessionOutput, sessports.KindBlockStarted, sessports.KindBlockClosed, sessports.KindSessionExited)

	if w.pinnedBlock && !block.Open() {
		// A closed block is its own output; nothing more will be written to it.
		text, err := s.cfg.Terminal.BlockText(ctx, block.ID)
		if err != nil {
			w.Close()
			return nil, err
		}
		w.initial, w.following = domain.Tail(text, p.Lines), false
		return w, nil
	}
	scr, err := s.cfg.Terminal.ScreenText(ctx, w.sessionID)
	if err != nil {
		w.Close()
		return nil, err
	}
	w.lines, w.following = p.Lines, true
	w.window(scr)
	if !w.pinnedBlock {
		latest, ok, err := s.cfg.Terminal.LatestBlock(ctx, w.sessionID)
		if err != nil {
			w.Close()
			return nil, err
		}
		if ok {
			w.blockID = latest.ID
		}
	}
	return w, nil
}

type outputWait struct {
	s         *Service
	matcher   *domain.Matcher
	timeout   time.Duration
	start     time.Time
	sub       *bus.Subscription
	sessionID string
	// blockID is the block a matched line is reported with: the one named, or the
	// session's most recent, then each block that starts while the wait follows.
	blockID     string
	pinnedBlock bool
	initial     []string
	open        string
	seq         uint64
	lines       int
	following   bool
	feed        domain.Feed
}

func (w *outputWait) Close() { w.sub.Close() }

// window takes a screen as the wait's starting point. A line still open at the cursor — a
// prompt — is not consumed: it is evaluated as it stands, trailing blanks and all, and seeds
// the feed, so the output that follows continues it rather than starting a line of its own.
func (w *outputWait) window(scr ports.Screen) {
	w.initial, w.seq, w.feed, w.open = domain.Tail(scr.Text, w.lines), scr.Seq, domain.Feed{}, ""
	w.matcher.Restart()
	if n := len(w.initial); n > 0 && scr.OpenLine != "" && w.initial[n-1] == strings.TrimRight(scr.OpenLine, " \t") {
		w.open = scr.OpenLine
		w.initial = w.initial[:n-1]
		w.feed.Seed(w.open)
	}
}

// evaluateWindow evaluates the window's complete lines, then its open one.
func (w *outputWait) evaluateWindow() (domain.Match, bool) {
	if m, ok := w.matcher.Lines(w.initial); ok {
		return m, true
	}
	return w.matcher.Partial(w.open)
}

func (w *outputWait) Wait(ctx context.Context) (ports.OutputResult, error) {
	defer w.Close()
	found := func(m domain.Match) (ports.OutputResult, error) {
		return ports.OutputResult{BlockID: w.blockID, MatchedLine: m.Line, LineNumber: m.Number}, nil
	}
	if m, ok := w.evaluateWindow(); ok {
		return found(m)
	}

	deadline := time.NewTimer(w.timeout - w.s.cfg.Now().Sub(w.start))
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ports.OutputResult{}, ctx.Err()
		case <-deadline.C:
			return ports.OutputResult{}, &domain.TimeoutError{LastState: w.matcher.Last(), Waited: w.s.cfg.Now().Sub(w.start)}
		case ev := <-w.sub.C():
			if !w.following {
				continue
			}
			m, ok, err := w.event(ctx, ev)
			if err != nil {
				return ports.OutputResult{}, err
			}
			if ok {
				return found(m)
			}
		}
	}
}

func (w *outputWait) event(ctx context.Context, ev bus.Event) (domain.Match, bool, error) {
	switch e := ev.(type) {
	case sessports.SessionOutput:
		if e.SessionID != w.sessionID || e.Seq <= w.seq {
			return domain.Match{}, false, nil
		}
		if e.Seq != w.seq+1 {
			// The bus dropped output: read the screen again rather than miss a line.
			return w.resync(ctx)
		}
		w.seq = e.Seq
		lines, partial := w.feed.Write(e.Data)
		if m, ok := w.matcher.Lines(lines); ok {
			return m, true, nil
		}
		m, ok := w.matcher.Partial(partial)
		return m, ok, nil
	case sessports.BlockStarted:
		if e.Block.SessionID == w.sessionID {
			if w.pinnedBlock {
				// The named block has ended and another began: it will get no more output.
				w.following = e.Block.ID == w.blockID
			} else {
				w.blockID = e.Block.ID
			}
		}
	case sessports.BlockClosed:
		if w.pinnedBlock && e.Block.ID == w.blockID {
			w.following = false
		}
	case sessports.SessionExited:
		if e.SessionID == w.sessionID {
			w.following = false
		}
	}
	return domain.Match{}, false, nil
}

func (w *outputWait) resync(ctx context.Context) (domain.Match, bool, error) {
	scr, err := w.s.cfg.Terminal.ScreenText(ctx, w.sessionID)
	if err != nil {
		return domain.Match{}, false, fmt.Errorf("waits: read the screen again: %w", err)
	}
	w.window(scr)
	m, ok := w.evaluateWindow()
	return m, ok, nil
}
