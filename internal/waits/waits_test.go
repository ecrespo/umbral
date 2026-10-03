package waits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/waits/domain"
	"github.com/ecrespo/umbral/internal/waits/ports"
)

// fakeThreads is a thread whose status the test sets.
type fakeThreads struct {
	mu     sync.Mutex
	status ports.ThreadStatus
	reads  int
	// ends is the runtime's memory of how turns ended.
	ends map[string]string
}

func (f *fakeThreads) TurnEnd(_ context.Context, _, turnID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	end, ok := f.ends[turnID]
	return end, ok, nil
}

func (f *fakeThreads) set(state, attention, turn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = ports.ThreadStatus{State: state, Attention: attention, TurnID: turn}
}

func (f *fakeThreads) Status(_ context.Context, id string) (ports.ThreadStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if id != "thr_1" {
		return ports.ThreadStatus{}, agentsdomain.ErrNotFound
	}
	return f.status, nil
}

// fakeTerminal is one session with a screen and blocks the test sets.
type fakeTerminal struct {
	mu     sync.Mutex
	screen string
	seq    uint64
	open   string
	reads  int
	blocks map[string]sessdomain.Block
	texts  map[string]string
	latest string
}

func (f *fakeTerminal) ScreenText(_ context.Context, id string) (ports.Screen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != "ses_1" {
		return ports.Screen{}, sessdomain.ErrNotFound
	}
	f.reads++
	return ports.Screen{Text: f.screen, Seq: f.seq, OpenLine: f.open}, nil
}

func (f *fakeTerminal) Block(_ context.Context, id string) (sessdomain.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blocks[id]
	if !ok {
		return sessdomain.Block{}, sessdomain.ErrNotFound
	}
	return b, nil
}

func (f *fakeTerminal) BlockText(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.texts[id], nil
}

func (f *fakeTerminal) LatestBlock(_ context.Context, _ string) (sessdomain.Block, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blocks[f.latest]
	return b, ok, nil
}

func newService(t *testing.T, threads *fakeThreads, term *fakeTerminal) (*Service, *bus.Bus) {
	t.Helper()
	b := bus.New()
	t.Cleanup(b.Close)
	s, err := New(Config{Bus: b, Threads: threads, Terminal: term, Backstop: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

// await runs a pinned thread wait in the background.
func await(t *testing.T, w ports.ThreadWait) <-chan struct {
	res ports.ThreadResult
	err error
} {
	t.Helper()
	out := make(chan struct {
		res ports.ThreadResult
		err error
	}, 1)
	go func() {
		res, err := w.Wait(context.Background())
		out <- struct {
			res ports.ThreadResult
			err error
		}{res, err}
	}()
	return out
}

func TestAWaitOnAThreadAlreadyInATargetReturnsAtOnce_REQ_AUT_001(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("idle", "done", "trn_a")
	s, _ := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"done"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.PinCurrent(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := w.Wait(t.Context())
	if err != nil || res.State != domain.StateDone || res.TurnID != "trn_a" || res.ThreadID != "thr_1" {
		t.Fatalf("Wait = %+v %v", res, err)
	}
}

// TestTheTurnEndingSettlesTheWait_REQ_AUT_001: the pinned turn's end, as the event reports
// it, settles the wait — even when the store already shows a later turn.
func TestTheTurnEndingSettlesTheWait_REQ_AUT_001(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_a")
	s, b := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"done", "stopped"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.PinCurrent(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := await(t, w)

	threads.set("running", "working", "trn_b") // a new turn already started
	b.Publish(agentsports.TurnFinished{ThreadID: "thr_1", TurnID: "trn_a", StopReason: agentsdomain.StopCancelled, EndState: "stopped"})
	r := <-got
	if r.err != nil || r.res.State != domain.StateStopped || r.res.TurnID != "trn_a" {
		t.Fatalf("Wait = %+v %v", r.res, r.err)
	}
}

// TestAReplacementTurnDoesNotSatisfyThePinnedWait_REQ_AUT_001: pinned to turn A until
// blocked, the wait ends when A ends done; turn B asking for approval afterwards is not A.
func TestAReplacementTurnDoesNotSatisfyThePinnedWait_REQ_AUT_001(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_a")
	s, b := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"blocked"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.PinCurrent(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := await(t, w)

	threads.set("idle", "done", "trn_a")
	b.Publish(agentsports.TurnFinished{ThreadID: "thr_1", TurnID: "trn_a", StopReason: agentsdomain.StopEndTurn, EndState: "done"})
	threads.set("awaiting_approval", "blocked", "trn_b")
	b.Publish(agentsports.ApprovalRequested{Approval: agentsdomain.Approval{ThreadID: "thr_1"}})
	r := <-got
	if r.err != nil || r.res.State != domain.StateDone || r.res.TurnID != "trn_a" {
		t.Fatalf("Wait = %+v %v, want turn A's done", r.res, r.err)
	}
}

func TestAnApprovalOfThePinnedTurnIsBlocked_REQ_AUT_001(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_a")
	s, b := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"blocked"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	w.Pin("trn_a")
	got := await(t, w)
	// The approval is answered before the wait reads the store: the event alone says blocked.
	b.Publish(agentsports.ApprovalRequested{Approval: agentsdomain.Approval{ThreadID: "thr_1"}})
	r := <-got
	if r.err != nil || r.res.State != domain.StateBlocked {
		t.Fatalf("Wait = %+v %v", r.res, r.err)
	}
}

// TestALostEventIsCaughtByTheBackstop: the bus drops under pressure, so a wait whose turn's
// end never arrives still settles from the store.
func TestALostEventIsCaughtByTheBackstop(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_a")
	s, _ := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"done"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	w.Pin("trn_a")
	got := await(t, w)
	threads.set("idle", "done", "trn_a")
	select {
	case r := <-got:
		if r.err != nil || r.res.State != domain.StateDone {
			t.Fatalf("Wait = %+v %v", r.res, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait never read the store again")
	}
}

// TestATimeoutCarriesTheLastState_REQ_AUT_004: the deadline is a *TimeoutError with the
// last state seen, and the wait does nothing else.
func TestATimeoutCarriesTheLastState_REQ_AUT_004(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_a")
	s, _ := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"done"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	w.Pin("trn_a")
	start := time.Now()
	_, err = w.Wait(t.Context())
	var te *domain.TimeoutError
	if !errors.As(err, &te) || !errors.Is(err, domain.ErrTimeout) || te.LastState != "working" {
		t.Fatalf("Wait = %v, want a timeout reporting working", err)
	}
	if took := time.Since(start); took < time.Second || took > 3*time.Second {
		t.Errorf("timed out after %v", took)
	}
}

func TestAWaitEndsWithItsContext(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_a")
	s, _ := newService(t, threads, nil)
	w, err := s.Thread(t.Context(), "thr_1", []string{"done"}, 60000)
	if err != nil {
		t.Fatal(err)
	}
	w.Pin("trn_a")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v", err)
	}
}

func TestThreadWaitValidates(t *testing.T) {
	threads := &fakeThreads{}
	s, _ := newService(t, threads, nil)
	if _, err := s.Thread(t.Context(), "thr_1", []string{"working"}, 5000); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("until working: %v", err)
	}
	if _, err := s.Thread(t.Context(), "thr_1", []string{"done"}, 10); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("timeout 10 ms: %v", err)
	}
	w, err := s.Thread(t.Context(), "thr_nope", []string{"done"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.PinCurrent(t.Context()); !errors.Is(err, agentsdomain.ErrNotFound) {
		t.Errorf("unknown thread: %v", err)
	}
}

func awaitOutput(t *testing.T, s *Service, p domain.OutputParams) <-chan struct {
	res ports.OutputResult
	err error
} {
	t.Helper()
	w, err := s.Output(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan struct {
		res ports.OutputResult
		err error
	}, 1)
	go func() {
		res, err := w.Wait(context.Background())
		out <- struct {
			res ports.OutputResult
			err error
		}{res, err}
	}()
	return out
}

// TestOutputAlreadyOnScreenMatches_REQ_AUT_003: what was on screen when the wait started is
// evaluated first, line by line, within `lines`.
func TestOutputAlreadyOnScreenMatches_REQ_AUT_003(t *testing.T) {
	term := &fakeTerminal{
		screen: "$ make\nbuilding\nready on :8080\n$ \n\n", seq: 7,
		blocks: map[string]sessdomain.Block{"blk_1": {ID: "blk_1", SessionID: "ses_1"}}, latest: "blk_1",
	}
	s, _ := newService(t, &fakeThreads{}, term)
	r := <-awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `ready on :\d+`, TimeoutMS: 2000})
	if r.err != nil || r.res.MatchedLine != "ready on :8080" || r.res.LineNumber != 3 || r.res.BlockID != "blk_1" {
		t.Fatalf("Wait = %+v %v", r.res, r.err)
	}

	r = <-awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `\$ make`, Lines: 2, TimeoutMS: 1000})
	if !errors.Is(r.err, domain.ErrTimeout) {
		t.Fatalf("a line above `lines` matched: %+v %v", r.res, r.err)
	}
}

// TestOutputAfterTheStartMatches_REQ_AUT_003: output that arrives later is followed, line
// by line, from the screen's sequence number on, and attributed to the block it ran in.
func TestOutputAfterTheStartMatches_REQ_AUT_003(t *testing.T) {
	term := &fakeTerminal{screen: "$ ", seq: 3}
	s, b := newService(t, &fakeThreads{}, term)
	got := awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `^PASS`, TimeoutMS: 3000})

	// Output the screen already held is not read twice.
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 3, Data: []byte("PASS stale\r\n")})
	b.Publish(sessports.BlockStarted{Block: sessdomain.Block{ID: "blk_9", SessionID: "ses_1"}})
	b.Publish(sessports.SessionOutput{SessionID: "ses_2", Seq: 4, Data: []byte("PASS elsewhere\r\n")})
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 4, Data: []byte("ok 1\r\nPA")})
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 5, Data: []byte("SS all\r\n")})
	r := <-got
	if r.err != nil || r.res.MatchedLine != "PASS all" || r.res.BlockID != "blk_9" || r.res.LineNumber != 3 {
		t.Fatalf("Wait = %+v %v", r.res, r.err)
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.reads != 1 {
		t.Fatalf("the screen was read %d times; output it already held is skipped, not a gap", term.reads)
	}
}

// TestALineHalfOnScreenIsContinued_REQ_AUT_003: a prompt the wait finds half written —
// the cursor still on it — is evaluated as it stands, trailing space included, and continued
// by the output that follows rather than cut in two.
func TestALineHalfOnScreenIsContinued_REQ_AUT_003(t *testing.T) {
	term := &fakeTerminal{screen: "$ login\nEnter pass\n\n", seq: 2, open: "Enter pass"}
	s, b := newService(t, &fakeThreads{}, term)
	got := awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `password: $`, TimeoutMS: 3000})
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 3, Data: []byte("word: ")})
	r := <-got
	if r.err != nil || r.res.MatchedLine != "Enter password: " || r.res.LineNumber != 2 {
		t.Fatalf("continued: %+v %v", r.res, r.err)
	}

	// Already complete on screen, with the trailing space the screen's text trims.
	term.screen, term.open = "Password:\n", "Password: "
	r = <-awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `Password: $`, TimeoutMS: 1000})
	if r.err != nil || r.res.MatchedLine != "Password: " {
		t.Fatalf("on screen: %+v %v", r.res, r.err)
	}

	// Continued after a trailing space: the space stays.
	term.screen, term.open = "Continue? [y/N]\n", "Continue? [y/N] "
	got = awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `\[y/N\] y$`, TimeoutMS: 3000})
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 3, Data: []byte("y")})
	if r = <-got; r.err != nil || r.res.MatchedLine != "Continue? [y/N] y" {
		t.Fatalf("after a space: %+v %v", r.res, r.err)
	}
}

// TestNoOpenLineMeansEveryLineIsComplete_REQ_AUT_003: without an open line at the cursor —
// a full-screen program's cursor mid-screen, or a cursor on a blank row — the screen's last
// line is complete, and what follows does not join it.
func TestNoOpenLineMeansEveryLineIsComplete_REQ_AUT_003(t *testing.T) {
	term := &fakeTerminal{screen: "foo\n    \n", seq: 1}
	s, b := newService(t, &fakeThreads{}, term)
	got := awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `^foobar`, TimeoutMS: 1000})
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 2, Data: []byte("bar\r\n")})
	if r := <-got; !errors.Is(r.err, domain.ErrTimeout) {
		t.Fatalf("a line that never existed matched: %+v %v", r.res, r.err)
	}
}

// TestAnOutputTimeoutReportsTheLastLine_REQ_AUT_004.
func TestAnOutputTimeoutReportsTheLastLine_REQ_AUT_004(t *testing.T) {
	term := &fakeTerminal{screen: "compiling\n", seq: 1}
	s, b := newService(t, &fakeThreads{}, term)
	got := awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `never`, TimeoutMS: 1000})
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 2, Data: []byte("linking\r\n")})
	r := <-got
	var te *domain.TimeoutError
	if !errors.As(r.err, &te) || te.LastState != "linking" {
		t.Fatalf("Wait = %+v %v, want a timeout reporting linking", r.res, r.err)
	}
}

// TestAClosedBlockIsReadFromItsOutput_REQ_AUT_003: a finished block's own output is what a
// block_id wait evaluates.
func TestAClosedBlockIsReadFromItsOutput_REQ_AUT_003(t *testing.T) {
	end := time.Now()
	term := &fakeTerminal{
		screen: "unrelated\n",
		blocks: map[string]sessdomain.Block{"blk_1": {ID: "blk_1", SessionID: "ses_1", State: sessdomain.BlockFinished, EndedAt: &end}},
		texts:  map[string]string{"blk_1": "a\nFAIL x\n"},
	}
	s, _ := newService(t, &fakeThreads{}, term)
	r := <-awaitOutput(t, s, domain.OutputParams{BlockID: "blk_1", Regex: `FAIL`, TimeoutMS: 1000})
	if r.err != nil || r.res.MatchedLine != "FAIL x" || r.res.LineNumber != 2 || r.res.BlockID != "blk_1" {
		t.Fatalf("Wait = %+v %v", r.res, r.err)
	}
}

func TestOutputWaitValidates(t *testing.T) {
	s, _ := newService(t, &fakeThreads{}, &fakeTerminal{})
	if _, err := s.Output(t.Context(), domain.OutputParams{SessionID: "ses_1", Regex: "(", TimeoutMS: 1000}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad regex: %v", err)
	}
	if _, err := s.Output(t.Context(), domain.OutputParams{SessionID: "ses_nope", Regex: "x", TimeoutMS: 1000}); !errors.Is(err, sessdomain.ErrNotFound) {
		t.Errorf("unknown session: %v", err)
	}
}

// TestAReplacedTurnAnswersWithItsOwnEnd_REQ_AUT_001: a wait whose first reading is already a
// later turn answers with the pinned turn's end — from its event, from the runtime's memory of
// it, or `unknown` when neither tells — never with the later turn's state.
func TestAReplacedTurnAnswersWithItsOwnEnd_REQ_AUT_001(t *testing.T) {
	t.Run("from its event", func(t *testing.T) {
		threads := &fakeThreads{}
		threads.set("running", "working", "trn_b")
		s, b := newService(t, threads, nil)
		w, _ := s.Thread(t.Context(), "thr_1", []string{"done"}, 5000)
		w.Pin("trn_a")
		got := await(t, w)
		time.Sleep(20 * time.Millisecond)
		b.Publish(agentsports.TurnFinished{ThreadID: "thr_1", TurnID: "trn_a", EndState: "stopped"})
		if r := <-got; r.err != nil || r.res.State != domain.StateStopped || r.res.TurnID != "trn_a" {
			t.Fatalf("Wait = %+v %v", r.res, r.err)
		}
	})
	t.Run("from the runtime's memory", func(t *testing.T) {
		threads := &fakeThreads{ends: map[string]string{"trn_a": "stopped"}}
		threads.set("idle", "done", "trn_b")
		s, _ := newService(t, threads, nil)
		w, _ := s.Thread(t.Context(), "thr_1", []string{"done"}, 5000)
		w.Pin("trn_a")
		if r := <-await(t, w); r.err != nil || r.res.State != domain.StateStopped {
			t.Fatalf("Wait = %+v %v", r.res, r.err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		threads := &fakeThreads{}
		threads.set("idle", "done", "trn_b")
		s, _ := newService(t, threads, nil)
		w, _ := s.Thread(t.Context(), "thr_1", []string{"done"}, 5000)
		w.Pin("trn_a")
		if r := <-await(t, w); r.err != nil || r.res.State != domain.StateUnknown || r.res.TurnID != "trn_a" {
			t.Fatalf("Wait = %+v %v", r.res, r.err)
		}
	})
}

// TestAShortWaitOnAReplacedTurnTimesOutUnknown_REQ_AUT_004: a replaced turn whose end is
// not known before the deadline reports `unknown` as its last state, never "".
func TestAShortWaitOnAReplacedTurnTimesOutUnknown_REQ_AUT_004(t *testing.T) {
	threads := &fakeThreads{}
	threads.set("running", "working", "trn_b")
	b := bus.New()
	t.Cleanup(b.Close)
	s, err := New(Config{Bus: b, Threads: threads, Backstop: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := s.Thread(t.Context(), "thr_1", []string{"done"}, 1000)
	w.Pin("trn_a")
	_, err = w.Wait(t.Context())
	var te *domain.TimeoutError
	if !errors.As(err, &te) || te.LastState != "unknown" {
		t.Fatalf("Wait = %v, want a timeout reporting unknown", err)
	}
}

// TestAGapRestartsTheNumbering_REQ_AUT_003: output the bus dropped makes the wait read the
// screen again, and that window's lines are numbered from 1.
func TestAGapRestartsTheNumbering_REQ_AUT_003(t *testing.T) {
	term := &fakeTerminal{screen: "a\nb\n", seq: 1}
	s, b := newService(t, &fakeThreads{}, term)
	got := awaitOutput(t, s, domain.OutputParams{SessionID: "ses_1", Regex: `^HIT$`, TimeoutMS: 3000})
	time.Sleep(20 * time.Millisecond)
	term.mu.Lock()
	term.screen, term.seq = "x\nHIT\n", 9
	term.mu.Unlock()
	b.Publish(sessports.SessionOutput{SessionID: "ses_1", Seq: 5, Data: []byte("lost\r\n")})
	if r := <-got; r.err != nil || r.res.MatchedLine != "HIT" || r.res.LineNumber != 2 {
		t.Fatalf("Wait = %+v %v", r.res, r.err)
	}
}
