package agents

import (
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
)

// stallWindows makes the stall windows short enough to test: 150 ms for a local model, 60 ms
// for a remote one, checked every 10 ms.
func stallWindows(cfg *Config) {
	cfg.StallLocal, cfg.StallRemote, cfg.StallTick = 150*time.Millisecond, 60*time.Millisecond, 10*time.Millisecond
}

// stalls are the thread.stalled events the rig's bus has seen.
func (r *rig) stalls() []ports.ThreadStalled {
	r.bus.mu.Lock()
	defer r.bus.mu.Unlock()
	var out []ports.ThreadStalled
	for _, ev := range r.bus.evs {
		if e, ok := ev.(ports.ThreadStalled); ok {
			out = append(out, e)
		}
	}
	return out
}

func eventually(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestStalledTurnNotifiedNotKilled_REQ_AUT_008: a running turn that produces nothing for its
// window is marked stalled, announced once with thread.stalled and counted, shows as stalled
// in thread.get and thread.list, and keeps running; activity clears the mark (delta
// `2026-10-wait-monitoring`, decisions 6 and 7).
func TestStalledTurnNotifiedNotKilled_REQ_AUT_008(t *testing.T) {
	r := newRigWith(t, stallWindows, answer("after the stall"))
	hold := make(chan struct{})
	r.models.hold = hold
	th := r.thread(t, domain.CreateParams{})
	res, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 2*time.Second, func() bool { return len(r.stalls()) > 0 }) {
		t.Fatal("no thread.stalled for a silent turn")
	}
	time.Sleep(100 * time.Millisecond) // several more ticks: still one announcement
	stalls := r.stalls()
	if len(stalls) != 1 {
		t.Fatalf("thread.stalled %d times for one stall", len(stalls))
	}
	if s := stalls[0]; s.ThreadID != th.ID || s.TurnID != res.TurnID || s.IdleMS < 60 || s.LastEvent != "turn_started" {
		t.Errorf("thread.stalled = %+v", s)
	}
	if n := r.metrics.stalledCount(); n != 1 {
		t.Errorf("umbral_waits_stalled_total counted %d", n)
	}
	got, err := r.rt.Get(t.Context(), th.ID)
	if err != nil || !got.Stalled || got.State != domain.StateRunning {
		t.Fatalf("thread.get during the stall: stalled %v, state %s, %v", got.Stalled, got.State, err)
	}
	list, err := r.rt.List(t.Context())
	if err != nil || len(list) != 1 || !list[0].Stalled {
		t.Errorf("thread.list during the stall: %+v %v", list, err)
	}
	if !r.rt.Stalled(th.ID) {
		t.Error("Stalled reports the stalled thread as not stalled")
	}

	// Not killed: the turn finishes when the model answers, and the mark goes.
	close(hold)
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("the stalled turn ended %s", end.StopReason)
	}
	if got, _ := r.rt.Get(t.Context(), th.ID); got.Stalled {
		t.Error("a finished turn is still marked stalled")
	}
}

// TestActivityKeepsATurnFromStalling_REQ_AUT_008: output from the thread's own pane is
// activity; a turn whose pane keeps writing is not stalled.
func TestActivityKeepsATurnFromStalling_REQ_AUT_008(t *testing.T) {
	r := newRigWith(t, stallWindows, answer("done"))
	hold := make(chan struct{})
	r.models.hold = hold
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	for range 20 { // 200 ms of output, past the 60 ms window
		r.rt.NoteOutput(th.ID)
		time.Sleep(10 * time.Millisecond)
	}
	if len(r.stalls()) != 0 {
		t.Fatalf("a turn whose pane kept writing was marked stalled: %+v", r.stalls())
	}
	close(hold)
	r.finished(t)
}

// TestALocalModelGetsTheLongerWindow_REQ_AUT_008: a thread whose model has a local candidate
// gets the local window, since a local model may take minutes to answer.
func TestALocalModelGetsTheLongerWindow_REQ_AUT_008(t *testing.T) {
	r := newRigWith(t, stallWindows, answer("done"))
	r.models.local = true
	hold := make(chan struct{})
	r.models.hold = hold
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // past the remote window, inside the local one
	if len(r.stalls()) != 0 {
		t.Fatalf("a local model's turn stalled on the remote window: %+v", r.stalls())
	}
	if !eventually(t, 2*time.Second, func() bool { return len(r.stalls()) > 0 }) {
		t.Fatal("a local model's turn never stalled")
	}
	close(hold)
	r.finished(t)
}

// TestAnApprovalIsNotAStall_REQ_AUT_008: a turn paused on an approval waits for the user and
// is not marked stalled however long it waits; answering restarts its clock.
func TestAnApprovalIsNotAStall_REQ_AUT_008(t *testing.T) {
	r := newRigWith(t, stallWindows, callTool("run", `{"path":"make test"}`), answer("tests pass"))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	r.rt.NoteOutput(th.ID)             // the pane redraws while the user decides: still awaiting
	time.Sleep(150 * time.Millisecond) // past the 60 ms window
	if len(r.stalls()) != 0 {
		t.Fatalf("a turn awaiting approval was marked stalled: %+v", r.stalls())
	}
	hold := make(chan struct{})
	r.models.mu.Lock()
	r.models.hold = hold
	r.models.mu.Unlock()
	if _, err := r.rt.Respond(t.Context(), domain.Response{ApprovalID: a.ID, Approve: true, Scope: domain.ScopeOnce}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 2*time.Second, func() bool { return len(r.stalls()) > 0 }) {
		t.Fatal("the resumed turn, silent past its window, never stalled")
	}
	if last := r.stalls()[0].LastEvent; last != "tool_result" && last != "approval" {
		t.Errorf("last_event %q after an answered approval", last)
	}
	close(hold)
	r.finished(t)
}

// TestALaterSilenceIsANewStall_REQ_AUT_008: activity clears a stall silently, and a silence
// after it is a new stall, announced again.
func TestALaterSilenceIsANewStall_REQ_AUT_008(t *testing.T) {
	r := newRigWith(t, stallWindows, answer("done"))
	hold := make(chan struct{})
	r.models.hold = hold
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, 2*time.Second, func() bool { return len(r.stalls()) == 1 }) {
		t.Fatal("no first stall")
	}
	r.rt.NoteOutput(th.ID)
	if r.rt.Stalled(th.ID) {
		t.Fatal("activity left the turn marked stalled")
	}
	if !eventually(t, 2*time.Second, func() bool { return len(r.stalls()) == 2 }) {
		t.Fatalf("a second silence was not a new stall: %d announcements", len(r.stalls()))
	}
	if last := r.stalls()[1].LastEvent; last != "output" {
		t.Errorf("the second stall's last_event = %q, want output", last)
	}
	close(hold)
	r.finished(t)
}
