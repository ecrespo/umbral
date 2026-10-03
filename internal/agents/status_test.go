package agents

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
)

// TestStatusNamesTheLatestTurn: what a wait pins is the thread's latest turn, read with its
// state in one go; a thread that never had a turn has none.
func TestStatusNamesTheLatestTurn(t *testing.T) {
	r := newRig(t, answer("hi"))
	th := r.thread(t, domain.CreateParams{})
	st, err := r.rt.Status(t.Context(), th.ID)
	if err != nil || st.TurnID != "" || st.State != domain.StateIdle || st.Attention != "idle" {
		t.Fatalf("before any turn: %+v %v", st, err)
	}
	res, _ := r.send(t, th.ID, "hello", "")
	st, err = r.rt.Status(t.Context(), th.ID)
	if err != nil || st.TurnID != res.TurnID || st.State != domain.StateIdle || st.Attention != "done" {
		t.Fatalf("after the turn: %+v %v, want turn %s idle/done", st, err, res.TurnID)
	}
	if _, err := r.rt.Status(t.Context(), "thr_nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

// TestTheTurnsEndCarriesWhatAWaitObserves: the end event says `done` after a normal end and
// `stopped` after a cancel — what the store was written with.
func TestTheTurnsEndCarriesWhatAWaitObserves(t *testing.T) {
	r := newRig(t, answer("hi"), callTool("run", `{"path":"sleep"}`))
	th := r.thread(t, domain.CreateParams{})
	if _, end := r.send(t, th.ID, "hello", ""); end.EndState != "done" {
		t.Fatalf("a normal end: %+v", end)
	}
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "run it"}); err != nil {
		t.Fatal(err)
	}
	r.requested(t)
	if _, err := r.rt.Cancel(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	if end := r.finished(t); end.EndState != "stopped" {
		t.Fatalf("a cancelled end: %+v", end)
	}
}

// TestSendWithWaitOnABlockedThreadIsRefused_REQ_AUT_002: a send that brings a wait, on a
// thread paused on an approval, is THREAD_BLOCKED and persists nothing; without a wait it
// is the ordinary CONFLICT.
func TestSendWithWaitOnABlockedThreadIsRefused_REQ_AUT_002(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make"}`))
	th := r.thread(t, domain.CreateParams{})
	const clientID = "01JZ0000000000000000000000"
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "build", ClientMsgID: clientID}); err != nil {
		t.Fatal(err)
	}
	r.requested(t)
	before, _ := r.rt.Messages(t.Context(), th.ID)

	_, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "again", RejectBlocked: true})
	if !errors.Is(err, domain.ErrThreadBlocked) {
		t.Fatalf("send with wait on a blocked thread: %v, want ErrThreadBlocked", err)
	}
	// Retrying the paused turn's own client_msg_id with a wait is refused too, rather than
	// answered with that turn: the blocked check comes before the id is looked up.
	_, err = r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "build", RejectBlocked: true, ClientMsgID: clientID})
	if !errors.Is(err, domain.ErrThreadBlocked) {
		t.Fatalf("with a client_msg_id: %v", err)
	}
	if _, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "again"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("send without wait: %v, want ErrConflict", err)
	}
	after, _ := r.rt.Messages(t.Context(), th.ID)
	if len(after) != len(before) {
		t.Fatalf("%d messages after the refusals, %d before", len(after), len(before))
	}
}

// TestTheRuntimeRemembersHowTurnsEnded: a wait whose turn a later one replaced asks the
// runtime how it ended — what its end event said.
func TestTheRuntimeRemembersHowTurnsEnded(t *testing.T) {
	r := newRig(t, answer("hi"), callTool("run", `{"path":"x"}`))
	th := r.thread(t, domain.CreateParams{})
	first, _ := r.send(t, th.ID, "hello", "")
	if end, ok := r.rt.TurnEnd(th.ID, first.TurnID); !ok || end != "done" {
		t.Fatalf("TurnEnd(first) = %q %v", end, ok)
	}
	second, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: th.ID, Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.rt.TurnEnd(th.ID, second.TurnID); ok {
		t.Fatal("a running turn has an end")
	}
	r.requested(t)
	if _, err := r.rt.Cancel(t.Context(), th.ID); err != nil {
		t.Fatal(err)
	}
	r.finished(t)
	if end, ok := r.rt.TurnEnd(th.ID, second.TurnID); !ok || end != "stopped" {
		t.Fatalf("TurnEnd(second) = %q %v", end, ok)
	}
	if _, ok := r.rt.TurnEnd("thr_other", first.TurnID); ok {
		t.Fatal("a turn answered for another thread")
	}
}

// TestAnEndThatWasNotWrittenIsUnknown: when the turn's end could not be written the store
// still says running, so the end a wait is told is `unknown`, not the state never written.
func TestAnEndThatWasNotWrittenIsUnknown(t *testing.T) {
	r := newRig(t, answer("hi"))
	th := r.thread(t, domain.CreateParams{})
	r.store.failFinish = true
	res, end := r.send(t, th.ID, "hello", "")
	if end.StopReason != domain.StopStorageError || end.EndState != "unknown" {
		t.Fatalf("end %+v", end)
	}
	if got, ok := r.rt.TurnEnd(th.ID, res.TurnID); !ok || got != "unknown" {
		t.Fatalf("TurnEnd = %q %v", got, ok)
	}
}

// TestTurnEndsForgetTheOldestPastTheBound: the memory keeps the last maxTurnEnds ends.
func TestTurnEndsForgetTheOldestPastTheBound(t *testing.T) {
	var e turnEnds
	for i := range maxTurnEnds + 1 {
		e.add("thr_1", fmt.Sprint("trn_", i), "done")
	}
	if _, ok := e.byTurn["trn_0"]; ok {
		t.Fatal("the oldest end was kept")
	}
	if _, ok := e.byTurn["trn_1"]; !ok {
		t.Fatal("the second oldest end was dropped")
	}
	if _, ok := e.byTurn[fmt.Sprint("trn_", maxTurnEnds)]; !ok || len(e.byTurn) != maxTurnEnds || len(e.order) != maxTurnEnds {
		t.Fatalf("%d ends, %d in order", len(e.byTurn), len(e.order))
	}
}
