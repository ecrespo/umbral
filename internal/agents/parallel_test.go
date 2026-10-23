package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// heldRig is a rig whose model calls wait on hold, with limit turn slots.
func heldRig(t *testing.T, limit, turns int) (*rig, chan struct{}) {
	t.Helper()
	scripts := make([][]llm.Event, turns)
	for i := range scripts {
		scripts[i] = answer("done")
	}
	r := newRigWith(t, func(c *Config) { c.MaxConcurrentTurns = limit }, scripts...)
	hold := make(chan struct{})
	r.models.hold = hold
	return r, hold
}

// waitCalls waits until the model has been called n times.
func (r *rig) waitCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.models.requests()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d model calls, want %d", len(r.models.requests()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// threads makes n threads.
func (r *rig) threads(t *testing.T, n int) []domain.Thread {
	t.Helper()
	out := make([]domain.Thread, 0, n)
	for range n {
		out = append(out, r.thread(t, domain.CreateParams{}))
	}
	return out
}

// sendOnly sends text and returns without waiting for the turn.
func (r *rig) sendOnly(t *testing.T, threadID string) {
	t.Helper()
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: threadID, Text: "go"}); err != nil {
		t.Fatalf("send to %s: %v", threadID, err)
	}
}

// TestThreeThreadsRunConcurrently_REQ_ORC_001: three threads' turns are all inside a model
// call at once — none waits for another to end.
func TestThreeThreadsRunConcurrently_REQ_ORC_001(t *testing.T) {
	r, hold := heldRig(t, 3, 3)
	threads := r.threads(t, 3)
	for _, th := range threads {
		r.sendOnly(t, th.ID)
	}
	// Every stream is held, so three calls are only possible if three turns run at once.
	r.waitCalls(t, 3)
	for _, th := range threads {
		if got, _ := r.rt.Get(t.Context(), th.ID); got.Queued {
			t.Fatalf("thread %s is queued under the limit", th.ID)
		}
	}
	close(hold)
	for range 3 {
		if end := r.finished(t); end.StopReason != domain.StopEndTurn {
			t.Fatalf("a turn ended %s", end.StopReason)
		}
	}
}

// TestAFourthTurnIsQueued_REQ_ORC_002: a turn over max_concurrent_turns is accepted, held with
// queued and attention working, and started when a slot frees, in arrival order.
func TestAFourthTurnIsQueued_REQ_ORC_002(t *testing.T) {
	r, hold := heldRig(t, 3, 5)
	threads := r.threads(t, 5)
	for _, th := range threads {
		r.sendOnly(t, th.ID)
	}
	r.waitCalls(t, 3)
	for _, th := range threads[3:] {
		got, err := r.rt.Get(t.Context(), th.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Queued || got.AttentionState != "working" || got.State != domain.StateRunning {
			t.Fatalf("an over-limit thread reads queued=%v attention=%s state=%s, want true, working, running",
				got.Queued, got.AttentionState, got.State)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.models.requests()); n != 3 {
		t.Fatalf("%d model calls with three slots, want 3", n)
	}

	close(hold)
	for range 5 {
		if end := r.finished(t); end.StopReason != domain.StopEndTurn {
			t.Fatalf("a turn ended %s", end.StopReason)
		}
	}
	for _, th := range threads {
		if got, _ := r.rt.Get(t.Context(), th.ID); got.Queued {
			t.Fatalf("thread %s is still queued after its turn ended", th.ID)
		}
	}
}

// TestHeldTurnsStartInArrivalOrder_REQ_ORC_002: with one slot, held turns run one after
// another in the order their sends arrived.
func TestHeldTurnsStartInArrivalOrder_REQ_ORC_002(t *testing.T) {
	r, hold := heldRig(t, 1, 4)
	threads := r.threads(t, 4)
	r.sendOnly(t, threads[0].ID)
	r.waitCalls(t, 1)
	// Sent in reverse id order, so the order cannot come from sorting ids.
	for _, i := range []int{3, 1, 2} {
		r.sendOnly(t, threads[i].ID)
	}
	close(hold)
	for range 4 {
		r.finished(t)
	}
	calls := r.models.requests()
	for i, want := range []int{0, 3, 1, 2} {
		if calls[i].ThreadID != threads[want].ID {
			t.Fatalf("call %d was thread %s, want %s (arrival order)", i, calls[i].ThreadID, threads[want].ID)
		}
	}
}

// TestASecondSendToAQueuedThreadConflicts_REQ_ORC_002: a held turn is a turn: another send to
// its thread is refused as it would be for a running one.
func TestASecondSendToAQueuedThreadConflicts_REQ_ORC_002(t *testing.T) {
	r, hold := heldRig(t, 1, 2)
	a, b := r.thread(t, domain.CreateParams{}), r.thread(t, domain.CreateParams{})
	r.sendOnly(t, a.ID)
	r.waitCalls(t, 1)
	r.sendOnly(t, b.ID)
	if got, _ := r.rt.Get(t.Context(), b.ID); !got.Queued {
		t.Fatal("the second thread's turn is not queued")
	}
	_, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: b.ID, Text: "again"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second send to a queued thread: %v, want ErrConflict", err)
	}
	close(hold)
	r.finished(t)
	r.finished(t)
}

// TestCancellingAQueuedTurnDropsIt_REQ_ORC_002: thread.cancel ends a held turn cancelled
// without a model call, leaves the thread stopped and not queued, and frees its place.
func TestCancellingAQueuedTurnDropsIt_REQ_ORC_002(t *testing.T) {
	r, hold := heldRig(t, 1, 3)
	a, b, c := r.thread(t, domain.CreateParams{}), r.thread(t, domain.CreateParams{}), r.thread(t, domain.CreateParams{})
	r.sendOnly(t, a.ID)
	r.waitCalls(t, 1)
	r.sendOnly(t, b.ID)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stoppedAt, err := r.rt.Cancel(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stoppedAt == nil {
		t.Fatal("cancelling a held turn reported nothing stopped")
	}
	end := r.finished(t)
	if end.ThreadID != b.ID || end.StopReason != domain.StopCancelled {
		t.Fatalf("turn_finished %s %s, want %s cancelled", end.ThreadID, end.StopReason, b.ID)
	}
	got, _ := r.rt.Get(t.Context(), b.ID)
	if got.State != domain.StateStopped || got.Queued {
		t.Fatalf("the cancelled thread reads state=%s queued=%v, want stopped and not queued", got.State, got.Queued)
	}

	// The dropped turn held no place: c waits behind a only, and runs once a ends.
	r.sendOnly(t, c.ID)
	close(hold)
	for range 2 {
		if end := r.finished(t); end.StopReason != domain.StopEndTurn {
			t.Fatalf("a turn ended %s", end.StopReason)
		}
	}
	for _, call := range r.models.requests() {
		if call.ThreadID == b.ID {
			t.Fatal("the cancelled held turn called the model")
		}
	}
}

// TestAQueuedTurnDoesNotStall_REQ_ORC_002: waiting for a slot is not inactivity
// (REQ-AUT-008): a held turn is never announced stalled.
func TestAQueuedTurnDoesNotStall_REQ_ORC_002(t *testing.T) {
	scripts := [][]llm.Event{answer("a"), answer("b")}
	r := newRigWith(t, func(c *Config) {
		c.MaxConcurrentTurns = 1
		c.StallLocal, c.StallRemote, c.StallTick = 20*time.Millisecond, 20*time.Millisecond, 5*time.Millisecond
	}, scripts...)
	hold := make(chan struct{})
	r.models.hold = hold
	a, b := r.thread(t, domain.CreateParams{}), r.thread(t, domain.CreateParams{})
	r.sendOnly(t, a.ID)
	r.waitCalls(t, 1)
	r.sendOnly(t, b.ID)
	time.Sleep(100 * time.Millisecond)
	if r.rt.Stalled(b.ID) {
		t.Fatal("a held turn was marked stalled")
	}
	close(hold)
	r.finished(t)
	r.finished(t)
}

// TestAWorktreeChangeRefusesAQueuedThread_REQ_ORC_002: a held turn counts as running for the
// worktree guard, so a merge or discard cannot move the directory it is about to run in
// (delta `2026-10-worktree-methods`, decision 3).
func TestAWorktreeChangeRefusesAQueuedThread_REQ_ORC_002(t *testing.T) {
	r, hold := heldRig(t, 1, 2)
	a, b := r.thread(t, domain.CreateParams{}), r.thread(t, domain.CreateParams{})
	r.sendOnly(t, a.ID)
	r.waitCalls(t, 1)
	r.sendOnly(t, b.ID)
	err := r.rt.WhileIdle(t.Context(), b.ID, func() error { return nil })
	if !errors.Is(err, domain.ErrTurnRunning) {
		t.Fatalf("a worktree change on a queued thread: %v, want ErrTurnRunning", err)
	}
	close(hold)
	r.finished(t)
	r.finished(t)
}

// queuedAnswer is a call that waits for its provider, is sent, then answers.
func queuedAnswer(text string) []llm.Event {
	return append([]llm.Event{{Kind: llm.EventQueued}, {Kind: llm.EventSent}}, answer(text)...)
}

// stallRig is a rig with 20 ms stall windows, whose streams pause after event pauseAfter.
func stallRig(t *testing.T, pauseAfter int, scripts ...[]llm.Event) (*rig, chan struct{}) {
	t.Helper()
	r := newRigWith(t, func(c *Config) {
		c.StallLocal, c.StallRemote, c.StallTick = 20*time.Millisecond, 20*time.Millisecond, 5*time.Millisecond
	}, scripts...)
	release := make(chan struct{})
	r.models.pause, r.models.pauseAfter = release, pauseAfter
	return r, release
}

// TestAWaitForTheProviderDoesNotStall_REQ_ORC_005: a model call queued behind other threads'
// calls for its provider is not a silent model: its turn is not marked stalled while it waits.
func TestAWaitForTheProviderDoesNotStall_REQ_ORC_005(t *testing.T) {
	r, release := stallRig(t, 0, queuedAnswer("late"))
	th := r.thread(t, domain.CreateParams{})
	r.sendOnly(t, th.ID)
	r.waitCalls(t, 1)
	time.Sleep(100 * time.Millisecond)
	if r.rt.Stalled(th.ID) {
		t.Fatal("a turn waiting for its provider was marked stalled")
	}
	close(release)
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("the turn ended %s", end.StopReason)
	}
}

// TestASentCallStallsAgain_REQ_ORC_005: once the call is sent its clock runs again, so a
// provider silent past the window after the wait is a stall like any other.
func TestASentCallStallsAgain_REQ_ORC_005(t *testing.T) {
	r, release := stallRig(t, 1, queuedAnswer("late"))
	th := r.thread(t, domain.CreateParams{})
	r.sendOnly(t, th.ID)
	r.waitCalls(t, 1)
	deadline := time.Now().Add(2 * time.Second)
	for !r.rt.Stalled(th.ID) {
		if time.Now().After(deadline) {
			t.Fatal("a sent call silent past its window was never marked stalled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	r.finished(t)
}

// TestACompactionWaitingForItsProviderDoesNotStall_REQ_ORC_005: the summarizing call of a
// compaction waits for its provider like the main one, and its turn does not stall meanwhile.
func TestACompactionWaitingForItsProviderDoesNotStall_REQ_ORC_005(t *testing.T) {
	r, release := stallRig(t, 0, queuedAnswer("a summary of the past"), queuedAnswer("fixed"))
	r.models.window = 2000
	th := r.thread(t, domain.CreateParams{})
	for range 20 {
		turn := newID("trn")
		r.store.messages = append(r.store.messages,
			domain.Message{ID: newID("msg"), ThreadID: th.ID, TurnID: turn, Role: domain.RoleUser, Content: strings.Repeat("q", 400)},
			domain.Message{ID: newID("msg"), ThreadID: th.ID, TurnID: turn, Role: domain.RoleAssistant, Content: strings.Repeat("a", 400)},
		)
	}
	r.sendOnly(t, th.ID)
	r.waitCalls(t, 1)
	time.Sleep(100 * time.Millisecond)
	if r.rt.Stalled(th.ID) {
		t.Fatal("a turn whose compaction waits for its provider was marked stalled")
	}
	if reqs := r.models.requests(); reqs[0].Class != "fast" {
		t.Fatalf("the paused call is %q, want the compaction's fast one", reqs[0].Class)
	}
	close(release)
	if end := r.finished(t); end.StopReason != domain.StopEndTurn {
		t.Fatalf("the turn ended %s", end.StopReason)
	}
}

// TestClosingTheRuntimeStopsAQueuedTurn_REQ_ORC_002: a daemon stop ends a held turn as it
// ends a running one: cancelled, the thread stopped and no longer queued.
func TestClosingTheRuntimeStopsAQueuedTurn_REQ_ORC_002(t *testing.T) {
	r, _ := heldRig(t, 1, 2)
	a, b := r.thread(t, domain.CreateParams{}), r.thread(t, domain.CreateParams{})
	r.sendOnly(t, a.ID)
	r.waitCalls(t, 1)
	r.sendOnly(t, b.ID)
	r.rt.Close()
	got, _ := r.store.Thread(t.Context(), b.ID)
	if got.State != domain.StateStopped || got.Queued {
		t.Fatalf("a held turn after Close: state=%s queued=%v, want stopped and not queued", got.State, got.Queued)
	}
	for range 2 {
		if end := r.finished(t); end.StopReason != domain.StopCancelled {
			t.Fatalf("a turn ended %s at Close, want cancelled", end.StopReason)
		}
	}
	if n := len(r.models.requests()); n != 1 {
		t.Fatalf("%d model calls, want only the running turn's", n)
	}
}
