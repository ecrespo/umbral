package api

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/bus"
	waitsdomain "github.com/ecrespo/umbral/internal/waits/domain"
	waitsports "github.com/ecrespo/umbral/internal/waits/ports"
)

// fakeWaits answers every wait once release is closed, or with err.
type fakeWaits struct {
	mu        sync.Mutex
	release   chan struct{}
	cancelled chan struct{}
	pinned    []string
	until     [][]string
	err       error
	output    []waitsdomain.OutputParams
}

func newFakeWaits() *fakeWaits {
	return &fakeWaits{release: make(chan struct{}), cancelled: make(chan struct{}, 4)}
}

type fakeThreadWait struct {
	f        *fakeWaits
	threadID string
	turn     string
}

func (w *fakeThreadWait) PinCurrent(context.Context) error { w.Pin("trn_current"); return nil }

func (w *fakeThreadWait) Pin(turnID string) {
	w.turn = turnID
	w.f.mu.Lock()
	w.f.pinned = append(w.f.pinned, turnID)
	w.f.mu.Unlock()
}

func (w *fakeThreadWait) Wait(ctx context.Context) (waitsports.ThreadResult, error) {
	select {
	case <-w.f.release:
	case <-ctx.Done():
		w.f.cancelled <- struct{}{}
		return waitsports.ThreadResult{}, ctx.Err()
	}
	if w.f.err != nil {
		return waitsports.ThreadResult{}, w.f.err
	}
	return waitsports.ThreadResult{ThreadID: w.threadID, TurnID: w.turn, State: waitsdomain.StateDone, WaitedMS: 12}, nil
}

func (w *fakeThreadWait) Close() {}

func (f *fakeWaits) Thread(_ context.Context, threadID string, until []string, timeoutMS int64) (waitsports.ThreadWait, error) {
	if _, err := waitsdomain.ParseTargets(until); err != nil {
		return nil, err
	}
	if _, err := waitsdomain.ParseTimeout(timeoutMS); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.until = append(f.until, until)
	f.mu.Unlock()
	return &fakeThreadWait{f: f, threadID: threadID}, nil
}

type fakeOutputWait struct{ f *fakeWaits }

func (w fakeOutputWait) Wait(ctx context.Context) (waitsports.OutputResult, error) {
	select {
	case <-w.f.release:
	case <-ctx.Done():
		return waitsports.OutputResult{}, ctx.Err()
	}
	if w.f.err != nil {
		return waitsports.OutputResult{}, w.f.err
	}
	return waitsports.OutputResult{MatchedLine: "PASS", LineNumber: 4}, nil
}

func (fakeOutputWait) Close() {}

func (f *fakeWaits) Output(_ context.Context, p waitsdomain.OutputParams) (waitsports.OutputWait, error) {
	if _, err := p.Validate(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.output = append(f.output, p)
	f.mu.Unlock()
	return fakeOutputWait{f: f}, nil
}

func waitRig(t *testing.T, kind ClientKind) (*fakeThreads, *fakeWaits, *client) {
	t.Helper()
	svc := &fakeThreads{thread: agentsdomain.Thread{ID: "thr_1", Mode: "normal", ModelClass: "code", Cwd: "/w"}}
	waits := newFakeWaits()
	s := testServerWithConfig(t, Config{Threads: svc, Waits: waits, Bus: bus.New()})
	c := dial(t, s)
	if resp := c.hello(s.Token(), kind); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	return svc, waits, c
}

// TestAWaitDoesNotHoldItsConnection: a wait is answered when it settles, and the requests
// sent after it on the same connection are answered meanwhile — the approval.respond that
// would unblock it among them.
func TestAWaitDoesNotHoldItsConnection(t *testing.T) {
	t.Parallel()
	_, waits, c := waitRig(t, ClientTUI)

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "thread.wait",
		"params": map[string]any{"thread_id": "thr_1", "until": []string{"done"}, "timeout_ms": 60000},
	})
	c.send(append(body, '\n'))
	if resp := c.call(11, "thread.get", map[string]any{"thread_id": "thr_1"}); string(resp.ID) != "11" || resp.Error != nil {
		t.Fatalf("the request after the wait: %+v", resp)
	}
	close(waits.release)
	resp := c.read()
	if string(resp.ID) != "10" || resp.Error != nil {
		t.Fatalf("the wait's answer: %+v", resp)
	}
	var got struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
		State    string `json:"state"`
		WaitedMS int64  `json:"waited_ms"`
	}
	if decodeResult(t, resp, &got); got.ThreadID != "thr_1" || got.TurnID != "trn_current" || got.State != "done" || got.WaitedMS != 12 {
		t.Fatalf("thread.wait = %v", resp.Result)
	}
}

// TestClosingTheConnectionEndsItsWaits: a client that hangs up leaves no wait behind.
func TestClosingTheConnectionEndsItsWaits(t *testing.T) {
	t.Parallel()
	_, waits, c := waitRig(t, ClientTUI)
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "thread.wait",
		"params": map[string]any{"thread_id": "thr_1", "until": []string{"done"}, "timeout_ms": 60000},
	})
	c.send(append(body, '\n'))
	time.Sleep(50 * time.Millisecond)
	_ = c.conn.Close()
	select {
	case <-waits.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait outlived its connection")
	}
}

// TestAWaitTimeoutCarriesLastState_REQ_AUT_004: TIMEOUT's data names the last state.
func TestAWaitTimeoutCarriesLastState_REQ_AUT_004(t *testing.T) {
	t.Parallel()
	_, waits, c := waitRig(t, ClientTUI)
	waits.err = &waitsdomain.TimeoutError{LastState: "working", Waited: time.Second}
	close(waits.release)
	resp := c.call(10, "thread.wait", map[string]any{"thread_id": "thr_1", "until": []string{"done"}, "timeout_ms": 1000})
	if resp.Error == nil || resp.Error.Code != codeTimeout || resp.Error.Data == nil || resp.Error.Data.LastState == nil || *resp.Error.Data.LastState != "working" {
		t.Fatalf("thread.wait = %+v", resp.Error)
	}
	if resp.Error.Data.TraceID != "" {
		t.Error("a timeout carries a trace id")
	}
}

// TestSendWithWaitPinsItsOwnTurn_REQ_AUT_001: thread.send with `wait` is one submission: the
// wait is validated before anything is sent, pinned to the turn the send started, and the
// answer carries final_state.
func TestSendWithWaitPinsItsOwnTurn_REQ_AUT_001(t *testing.T) {
	t.Parallel()
	svc, waits, c := waitRig(t, ClientCLI)
	close(waits.release)

	resp := c.call(10, "thread.send", map[string]any{"thread_id": "thr_1", "text": "x", "wait": map[string]any{"until": []string{"working"}, "timeout_ms": 5000}})
	if resp.Error == nil || resp.Error.Code != codeValidationError || len(svc.sent) != 0 {
		t.Fatalf("an invalid wait: %+v, %d sent", resp.Error, len(svc.sent))
	}

	resp = c.call(11, "thread.send", map[string]any{"thread_id": "thr_1", "text": "x", "wait": map[string]any{"until": []string{"done", "stopped"}, "timeout_ms": 5000}})
	if resp.Error != nil {
		t.Fatalf("thread.send with wait: %+v", resp.Error)
	}
	var got struct {
		TurnID     string  `json:"turn_id"`
		MessageID  string  `json:"message_id"`
		FinalState *string `json:"final_state"`
	}
	if decodeResult(t, resp, &got); got.TurnID != "trn_1" || got.FinalState == nil || *got.FinalState != "done" {
		t.Fatalf("thread.send = %v", resp.Result)
	}
	if len(svc.sent) != 1 || !svc.sent[0].RejectBlocked || len(waits.pinned) != 1 || waits.pinned[0] != "trn_1" {
		t.Fatalf("sent %+v, pinned %v", svc.sent, waits.pinned)
	}

	resp = c.call(12, "thread.send", map[string]any{"thread_id": "thr_1", "text": "y"})
	if resp.Error != nil || len(svc.sent) != 2 || svc.sent[1].RejectBlocked {
		t.Fatalf("a send without wait: %+v %+v", resp.Error, svc.sent)
	}
	var plain map[string]any
	decodeResult(t, resp, &plain)
	if _, has := plain["final_state"]; has {
		t.Errorf("a send without wait carries final_state: %v", plain)
	}
}

// TestSendWaitRejectsBlockedOnTheWire_REQ_AUT_002: the runtime's refusal is THREAD_BLOCKED,
// and no wait is pinned.
func TestSendWaitRejectsBlockedOnTheWire_REQ_AUT_002(t *testing.T) {
	t.Parallel()
	svc, waits, c := waitRig(t, ClientTUI)
	svc.sendErr = agentsdomain.ErrThreadBlocked
	resp := c.call(10, "thread.send", map[string]any{"thread_id": "thr_1", "text": "x", "wait": map[string]any{"until": []string{"done"}, "timeout_ms": 5000}})
	if resp.Error == nil || resp.Error.Code != codeThreadBlocked || resp.Error.Data.DomainCode != "THREAD_BLOCKED" || len(waits.pinned) != 0 {
		t.Fatalf("thread.send = %+v, pinned %v", resp.Error, waits.pinned)
	}
}

// TestWaitOutputOnTheWire_REQ_AUT_003: block.wait_output is §2's block.* for every kind; one
// of session_id and block_id; the answer is the matched line with its number.
func TestWaitOutputOnTheWire_REQ_AUT_003(t *testing.T) {
	t.Parallel()
	_, waits, c := waitRig(t, ClientCLI)
	resp := c.call(10, "block.wait_output", map[string]any{"session_id": "ses_1", "block_id": "blk_1", "regex": "x", "timeout_ms": 1000})
	if resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Fatalf("both ids: %+v", resp.Error)
	}
	close(waits.release)
	resp = c.call(11, "block.wait_output", map[string]any{"session_id": "ses_1", "regex": "^PASS", "lines": 50, "timeout_ms": 1000})
	if resp.Error != nil {
		t.Fatalf("block.wait_output: %+v", resp.Error)
	}
	var got struct {
		BlockID     *string `json:"block_id"`
		MatchedLine string  `json:"matched_line"`
		LineNumber  int     `json:"line_number"`
	}
	if decodeResult(t, resp, &got); got.BlockID != nil || got.MatchedLine != "PASS" || got.LineNumber != 4 {
		t.Fatalf("block.wait_output = %v", resp.Result)
	}
	if p := waits.output[0]; p.SessionID != "ses_1" || p.Lines != 50 || p.Regex != "^PASS" {
		t.Fatalf("params %+v", p)
	}
}

// TestThreadWaitIsInteractive: §2's cli row has no thread.wait.
func TestThreadWaitIsInteractive(t *testing.T) {
	t.Parallel()
	_, _, c := waitRig(t, ClientCLI)
	resp := c.call(10, "thread.wait", map[string]any{"thread_id": "thr_1", "until": []string{"done"}, "timeout_ms": 1000})
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("thread.wait from umb = %+v", resp.Error)
	}
}

// TestWaitsWithoutTheEngineAreNotImplemented: a daemon without waits answers NOT_IMPLEMENTED
// for all three, a send with wait included, and sends nothing.
func TestWaitsWithoutTheEngineAreNotImplemented(t *testing.T) {
	t.Parallel()
	svc := &fakeThreads{thread: agentsdomain.Thread{ID: "thr_1"}}
	s := testServerWithConfig(t, Config{Threads: svc, Bus: bus.New()})
	c := dial(t, s)
	c.hello(s.Token(), ClientTUI)
	for i, call := range []struct {
		method string
		params map[string]any
	}{
		{"thread.wait", map[string]any{"thread_id": "thr_1", "until": []string{"done"}, "timeout_ms": 1000}},
		{"block.wait_output", map[string]any{"session_id": "s", "regex": "x", "timeout_ms": 1000}},
		{"thread.send", map[string]any{"thread_id": "thr_1", "text": "x", "wait": map[string]any{"until": []string{"done"}, "timeout_ms": 1000}}},
	} {
		if resp := c.call(10+i, call.method, call.params); resp.Error == nil || resp.Error.Code != codeNotImplemented {
			t.Errorf("%s = %+v, want NOT_IMPLEMENTED", call.method, resp.Error)
		}
	}
	if len(svc.sent) != 0 {
		t.Fatal("a send with a wait reached the runtime")
	}
}

// TestAnExplicitZeroLinesIsRefused: `lines` is 1-2000 when given; only its absence is the
// default (API §5.30).
func TestAnExplicitZeroLinesIsRefused(t *testing.T) {
	t.Parallel()
	_, waits, c := waitRig(t, ClientTUI)
	resp := c.call(10, "block.wait_output", map[string]any{"session_id": "ses_1", "regex": "x", "lines": 0, "timeout_ms": 1000})
	if resp.Error == nil || resp.Error.Code != codeValidationError || len(waits.output) != 0 {
		t.Fatalf("lines 0 = %+v", resp.Error)
	}
}

// TestAWaitSentAsANotificationIsNotKept: a wait nobody can be answered about ends at once.
func TestAWaitSentAsANotificationIsNotKept(t *testing.T) {
	t.Parallel()
	_, waits, c := waitRig(t, ClientTUI)
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "thread.wait",
		"params": map[string]any{"thread_id": "thr_1", "until": []string{"done"}, "timeout_ms": 60000},
	})
	c.send(append(body, '\n'))
	select {
	case <-waits.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("a wait sent as a notification kept running")
	}
	if resp := c.call(11, "thread.get", map[string]any{"thread_id": "thr_1"}); string(resp.ID) != "11" {
		t.Fatalf("the next answer is %+v", resp)
	}
}
