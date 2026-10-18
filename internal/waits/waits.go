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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// NewID makes a wait's id (`wt_` + ULID); nil numbers them.
	NewID func() string
	Now   func() time.Time
}

// Service is the wait engine.
type Service struct {
	cfg Config
	// active counts the waits open now: umbral_waits_active (REQ-OBS-004).
	active atomic.Int64

	// mu guards the registry: every open wait by id, and how many each connection holds
	// (REQ-AUT-005, REQ-AUT-006).
	mu     sync.Mutex
	open   map[string]*entry
	perCon map[string]int
	seq    int64
}

// entry is one registered wait. cancelled is closed by wait.cancel.
type entry struct {
	info      domain.Info
	cancelled chan struct{}
	once      sync.Once
}

// admit registers a wait for connID unless the connection already holds MaxPerConnection,
// before anything is subscribed or read (delta `2026-10-wait-monitoring`, decision 2).
func (s *Service) admit(connID string, info domain.Info) (*entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perCon[connID] >= domain.MaxPerConnection {
		return nil, domain.ErrLimit
	}
	if s.cfg.NewID != nil {
		info.ID = s.cfg.NewID()
	} else {
		s.seq++
		info.ID = fmt.Sprintf("wt_%026d", s.seq)
	}
	info.ConnectionID, info.StartedAt = connID, s.cfg.Now()
	e := &entry{info: info, cancelled: make(chan struct{})}
	s.open[info.ID] = e
	s.perCon[connID]++
	return e, nil
}

func (s *Service) release(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.open[e.info.ID]; !ok {
		return
	}
	delete(s.open, e.info.ID)
	if s.perCon[e.info.ConnectionID]--; s.perCon[e.info.ConnectionID] <= 0 {
		delete(s.perCon, e.info.ConnectionID)
	}
}

// List implements ports.Waits.
func (s *Service) List() []domain.Info {
	s.mu.Lock()
	items := make([]domain.Info, 0, len(s.open))
	for _, e := range s.open {
		items = append(items, e.info)
	}
	s.mu.Unlock()
	now := s.cfg.Now()
	slices.SortFunc(items, func(a, b domain.Info) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	for i := range items {
		items[i].AgeMS = now.Sub(items[i].StartedAt).Milliseconds()
		if items[i].ThreadID != "" && s.cfg.Threads != nil {
			items[i].Stalled = s.cfg.Threads.Stalled(items[i].ThreadID)
		}
	}
	return items
}

// Cancel implements ports.Waits. It touches nothing the wait observes (REQ-AUT-007).
func (s *Service) Cancel(waitID string) error {
	s.mu.Lock()
	e, ok := s.open[waitID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, waitID)
	}
	// Out of the registry at once: a second cancel, or a list, no longer sees a wait that
	// is only waiting to answer CANCELLED. release then finds nothing to remove.
	s.release(e)
	e.once.Do(func() { close(e.cancelled) })
	return nil
}

// Active is how many waits are open now (REQ-OBS-004).
func (s *Service) Active() int64 { return s.active.Load() }

// opened counts a wait from its subscription on, and returns its Close: the subscription's,
// the count's and the registry's, once however often it is called.
func (s *Service) opened(sub *bus.Subscription, e *entry) func() {
	s.active.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			sub.Close()
			s.release(e)
			s.active.Add(-1)
		})
	}
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
	return &Service{cfg: cfg, open: map[string]*entry{}, perCon: map[string]int{}}, nil
}

var _ ports.Waits = (*Service)(nil)

// Thread starts a thread wait: it validates, then subscribes before anything is read, so an
// event published between the caller's pin and its wait is not lost.
func (s *Service) Thread(_ context.Context, connID, threadID string, until []string, timeoutMS int64) (ports.ThreadWait, error) {
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
	e, err := s.admit(connID, domain.Info{ThreadID: threadID, Until: slices.Clone(until)})
	if err != nil {
		return nil, err
	}
	w := &threadWait{
		s: s, threadID: threadID, targets: targets, timeout: timeout, start: s.cfg.Now(), cancelled: e.cancelled,
		sub: s.cfg.Bus.Subscribe(agentsports.KindThreadTurnFinished, agentsports.KindApprovalRequested,
			agentsports.KindAttentionChanged),
	}
	w.close = s.opened(w.sub, e)
	return w, nil
}

type threadWait struct {
	s        *Service
	close    func()
	threadID string
	targets  domain.Targets
	timeout  time.Duration
	start    time.Time
	sub      *bus.Subscription
	tracker  *domain.Tracker
	// cancelled is closed by wait.cancel.
	cancelled <-chan struct{}
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

func (w *threadWait) Close() { w.close() }

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
		case <-w.cancelled:
			return ports.ThreadResult{}, domain.ErrCancelled
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
	case agentsports.ThreadAttentionChanged:
		// A client focusing a `done` thread is the one change that needs no turn (§5.29).
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
func (s *Service) Output(ctx context.Context, connID string, p domain.OutputParams) (ports.OutputWait, error) {
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
	e, err := s.admit(connID, domain.Info{BlockID: p.BlockID, SessionID: p.SessionID, Regex: p.Regex, Until: []string{}})
	if err != nil {
		return nil, err
	}
	w := &outputWait{s: s, matcher: matcher, timeout: timeout, start: s.cfg.Now(), sessionID: p.SessionID, cancelled: e.cancelled}

	var block sessdomain.Block
	if p.BlockID != "" {
		block, err = s.cfg.Terminal.Block(ctx, p.BlockID)
		if err != nil {
			s.release(e)
			return nil, err
		}
		w.sessionID, w.blockID, w.pinnedBlock = block.SessionID, block.ID, true
	}
	w.sub = s.cfg.Bus.SubscribeBuffered(outputBuffer,
		sessports.KindSessionOutput, sessports.KindBlockStarted, sessports.KindBlockClosed, sessports.KindSessionExited)
	w.close = s.opened(w.sub, e)

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
	close     func()
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
	// cancelled is closed by wait.cancel.
	cancelled <-chan struct{}
}

func (w *outputWait) Close() { w.close() }

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
		case <-w.cancelled:
			return ports.OutputResult{}, domain.ErrCancelled
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
