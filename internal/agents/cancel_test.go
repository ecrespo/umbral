package agents

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
)

// TestCancelStopsARunningTurn_REQ_AGT_007: thread.cancel stops the turn where it is — here
// waiting on the model — and returns once it has ended: turn_finished says cancelled, the
// thread is stopped, and the next send runs a new turn.
func TestCancelStopsARunningTurn_REQ_AGT_007(t *testing.T) {
	r := newRig(t, answer("never streamed"), answer("again"))
	hold := make(chan struct{})
	r.models.hold = hold
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stoppedAt, err := r.rt.Cancel(ctx, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Cancel returns once the turn has recorded its end, not merely once it was asked to.
	if got, _ := r.store.Thread(t.Context(), th.ID); got.State != domain.StateStopped {
		t.Fatalf("thread.cancel returned with the thread %s, want stopped", got.State)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("thread.cancel took %v", took)
	}
	if stoppedAt == nil {
		t.Fatal("a cancel that stopped a turn reported nothing stopped")
	}
	end := r.finished(t)
	if end.StopReason != domain.StopCancelled {
		t.Fatalf("stop %s, want cancelled", end.StopReason)
	}
	got, _ := r.store.Thread(t.Context(), th.ID)
	if got.State != domain.StateStopped {
		t.Fatalf("state %s, want stopped", got.State)
	}

	close(hold)
	r.models.mu.Lock()
	r.models.hold = nil
	r.models.mu.Unlock()
	if _, end := r.send(t, th.ID, "again", ""); end.StopReason != domain.StopEndTurn {
		t.Fatalf("the send after a cancel: %s", end.StopReason)
	}
}

// TestCancelWithNoTurnStopsNothing_REQ_AGT_007: a cancel that finds no turn running — one that
// lost the race with the turn's own end — changes nothing and reports nothing stopped; an
// unknown thread is NOT_FOUND.
func TestCancelWithNoTurnStopsNothing_REQ_AGT_007(t *testing.T) {
	r := newRig(t, answer("done"))
	th := r.thread(t, domain.CreateParams{})
	r.send(t, th.ID, "hi", "")
	before, _ := r.store.Thread(t.Context(), th.ID)

	stoppedAt, err := r.rt.Cancel(t.Context(), th.ID)
	if err != nil || stoppedAt != nil {
		t.Fatalf("cancel of an idle thread = %v, %v; want nothing stopped", stoppedAt, err)
	}
	after, _ := r.store.Thread(t.Context(), th.ID)
	if after.State != before.State {
		t.Fatalf("an idle cancel moved the thread from %s to %s", before.State, after.State)
	}
	if _, err := r.rt.Cancel(t.Context(), "thr_"+ulid1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown thread: %v", err)
	}
}

// TestCancelWhileAwaitingApproval_REQ_AGT_007: API §7's awaiting_approval → stopped. The
// pending approval expires and its tool never runs.
func TestCancelWhileAwaitingApproval_REQ_AGT_007(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make"}`))
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "build"}); err != nil {
		t.Fatal(err)
	}
	a := r.requested(t)
	if _, err := r.rt.Cancel(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	if end := r.finished(t); end.StopReason != domain.StopCancelled {
		t.Fatalf("stop %s", end.StopReason)
	}
	got, _ := r.store.Approval(t.Context(), a.ID)
	thread, _ := r.store.Thread(t.Context(), th.ID)
	if got.State != domain.ApprovalExpired || thread.State != domain.StateStopped || r.tools.tools["run"].ran.Load() != 0 {
		t.Fatalf("approval %s, thread %s, ran %d", got.State, thread.State, r.tools.tools["run"].ran.Load())
	}
}

// TestACancelRacingTheTurnsOwnEndStopsNothing_REQ_AGT_007: a cancel that finds the turn while
// it is already recording its own end — `end_turn` here — did not stop it. It answers null,
// and the thread is idle as the turn left it.
func TestACancelRacingTheTurnsOwnEndStopsNothing_REQ_AGT_007(t *testing.T) {
	r := newRig(t, answer("done"))
	th := r.thread(t, domain.CreateParams{})
	type result struct {
		at  *int64
		err error
	}
	got := make(chan result, 1)
	r.store.beforeFinish = func() {
		r.store.beforeFinish = nil
		go func() {
			at, err := r.rt.Cancel(t.Context(), th.ID)
			got <- result{at, err}
		}()
		// Let the cancel find the turn before its end is written.
		time.Sleep(100 * time.Millisecond)
	}
	_, end := r.send(t, th.ID, "hi", "")
	res := <-got
	if res.err != nil || res.at != nil {
		t.Fatalf("a cancel that lost the race = %v, %v; want null", res.at, res.err)
	}
	if end.StopReason != domain.StopEndTurn {
		t.Fatalf("stop %s, want end_turn", end.StopReason)
	}
	if thread, _ := r.store.Thread(t.Context(), th.ID); thread.State != domain.StateIdle {
		t.Fatalf("state %s, want idle", thread.State)
	}
}

// TestStoppedAtIsTheRecordedEnd_REQ_AGT_007: stopped_at is the moment the turn's end was
// written, the thread's updated_at, not a later reading of the clock.
func TestStoppedAtIsTheRecordedEnd_REQ_AGT_007(t *testing.T) {
	r := newRig(t, answer("never"))
	var clock atomic.Int64
	r.rt.cfg.Now = func() time.Time { return time.UnixMilli(clock.Add(1000)) }
	r.models.hold = make(chan struct{})
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	at, err := r.rt.Cancel(t.Context(), th.ID)
	if err != nil || at == nil {
		t.Fatalf("cancel = %v, %v", at, err)
	}
	thread, _ := r.store.Thread(t.Context(), th.ID)
	if *at != thread.UpdatedAt {
		t.Fatalf("stopped_at %d, the end was written at %d", *at, thread.UpdatedAt)
	}
}

// TestClosingTheDaemonStopsTheTurn_REQ_AGT_007: a turn the daemon's shutdown cancels ends
// `cancelled` and leaves the thread `stopped`, as a restart's recovery would.
func TestClosingTheDaemonStopsTheTurn_REQ_AGT_007(t *testing.T) {
	r := newRig(t, answer("never"))
	r.models.hold = make(chan struct{})
	th := r.thread(t, domain.CreateParams{})
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	r.rt.Close()
	if end := r.finished(t); end.StopReason != domain.StopCancelled {
		t.Fatalf("stop %s, want cancelled", end.StopReason)
	}
	if thread, _ := r.store.Thread(t.Context(), th.ID); thread.State != domain.StateStopped {
		t.Fatalf("state %s, want stopped", thread.State)
	}
}
