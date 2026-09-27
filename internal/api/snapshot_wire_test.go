package api

import (
	"encoding/json"
	"testing"
	"time"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// TestSnapshotCarriesSeq_REQ_API_001 is the criterion: the focused ids, the records, each
// tab's layout, and the `seq` of the last notification the result contains.
func TestSnapshotCarriesSeq_REQ_API_001(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	tree := &fakeTree{
		tree: sample, focusedWS: "w1", focusedTab: "w1:t1",
		layout: wsdomain.Layout{WorkspaceID: "w1", TabID: "w1:t1", FocusedPaneID: "w1:p1"},
	}
	s := testServerWithTree(t, tree)
	c := dialTree(t, s)

	// The counter is advanced without emitting anything, so it is somewhere other than zero
	// and a handler reporting a constant would show. Broadcasting instead would queue
	// notifications ahead of the response on this same connection, and `call` reads the
	// next message rather than the next *reply* — which is how the first version of this
	// test failed while the handler was right.
	for range 3 {
		s.nextSeq()
	}
	dispatched := s.currentSeq()

	got := resultOf(t, c.call(2, "session.snapshot", nil))
	for _, key := range []string{"seq", "focused", "workspaces", "tabs", "panes", "layouts", "threads"} {
		if _, ok := got[key]; !ok {
			t.Errorf("result has no %q; §5.3 lists it", key)
		}
	}

	var seq uint64
	if err := json.Unmarshal(got["seq"], &seq); err != nil {
		t.Fatalf("seq is not a number: %v", err)
	}
	if seq != dispatched {
		t.Errorf("seq = %d, want %d: the counter as of before the tree was read", seq, dispatched)
	}

	focused := field(t, got, "focused")
	for _, key := range []string{"workspace_id", "tab_id", "thread_id"} {
		if _, ok := focused[key]; !ok {
			t.Errorf("focused has no %q; §5.3 types all three", key)
		}
	}
	if string(focused["thread_id"]) != "null" {
		t.Errorf("thread_id = %s, want null in F0", focused["thread_id"])
	}

	// Threads are absent in F0 and the key is an empty array rather than missing, so a
	// client iterating the result does not have to know which build it is talking to.
	if string(got["threads"]) != "[]" {
		t.Errorf("threads = %s, want []", got["threads"])
	}
	for _, key := range []string{"workspaces", "tabs", "panes", "layouts"} {
		var items []json.RawMessage
		if err := json.Unmarshal(got[key], &items); err != nil {
			t.Errorf("%s is not an array: %v", key, err)
			continue
		}
		if len(items) != 1 {
			t.Errorf("%s has %d entries, want 1", key, len(items))
		}
	}
}

// TestTheSnapshotSeqIsReadBeforeTheTree is the ordering argument, and the reason it is a test
// rather than a comment.
//
// Reading the counter *after* the tree would let an event dispatched during the read carry a
// number below the reported one: the client discards it as already contained, and the tree it
// reconstructs is missing it. Nothing fails, nothing logs, and the two states differ forever.
// Here the tree read itself dispatches an event, which is what an implementation with the
// wrong order cannot survive.
func TestTheSnapshotSeqIsReadBeforeTheTree(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	tree := &fakeTree{tree: sample}
	s := testServerWithTree(t, tree)
	c := dialTree(t, s)

	before := s.currentSeq()
	// Every call to Snapshot bumps the counter, standing in for an event that lands while
	// the tree is being read.
	tree.onSnapshot = func() { s.nextSeq() }

	got := resultOf(t, c.call(2, "session.snapshot", nil))
	var seq uint64
	if err := json.Unmarshal(got["seq"], &seq); err != nil {
		t.Fatalf("seq: %v", err)
	}
	if seq != before {
		t.Errorf("seq = %d, want %d: an event that landed during the read must stay above the "+
			"reported number, or the client discards what the snapshot does not contain",
			seq, before)
	}
}

// TestEveryNotificationCarriesAnIncreasingSeq_REQ_API_002 pins the envelope counter itself:
// present on every notification, increasing along this path, and the same number for two
// clients.
//
// "Along this path" is the whole caveat. These are control notifications, which `notify`
// writes to the socket directly, so on a connection with no subscription the arrival order is
// the assignment order. It is not the general guarantee: decision 5 of the
// `2026-09-notification-sequencing` delta says the counter is monotonic in assignment only,
// because output waits in the queue of §8 while control notifications do not.
// TestSeqIsAssignedAtDispatchNotAtDelivery_REQ_API_002 covers the case this one cannot.
func TestEveryNotificationCarriesAnIncreasingSeq_REQ_API_002(t *testing.T) {
	t.Parallel()

	sample := sampleTree()
	s := testServerWithTree(t, &fakeTree{tree: sample})
	first := dialTree(t, s)
	second := dialTree(t, s)

	const sent = 5
	for range sent {
		s.broadcast("workspace.updated", toWireWorkspace(sample.Workspace), s.nextSeq())
	}

	read := func(c *client) []uint64 {
		t.Helper()
		out := make([]uint64, 0, sent)
		for range sent {
			out = append(out, c.readNotificationSeq(t))
		}
		return out
	}
	firstSeqs, secondSeqs := read(first), read(second)

	for i, seq := range firstSeqs {
		if seq == 0 {
			t.Errorf("notification %d carries seq 0; §6 numbers every one", i)
		}
		if i > 0 && seq <= firstSeqs[i-1] {
			t.Errorf("seq went %d then %d; on the direct write path the numbers arrive in the "+
				"order they were assigned", firstSeqs[i-1], seq)
		}
		// §6: "shared by all subscribers" — the same event, the same number, for everyone.
		if secondSeqs[i] != seq {
			t.Errorf("the same event reached one client as seq %d and the other as %d",
				seq, secondSeqs[i])
		}
	}
}

// TestOutputIsNotDiscardedOnTheEnvelopeSeq pins the decision a reasonable reading of the old
// §6 sentence would have got wrong.
//
// `session.snapshot` carries no screen, so its `seq` cannot have contained any output. A
// client that applied the discard rule to `session.output` would throw away bytes it had not
// seen — and the two numbers are both called `seq`, one in the envelope and one in the
// parameters, which is exactly how that mistake gets made.
func TestOutputIsNotDiscardedOnTheEnvelopeSeq(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := c.call(2, "session.subscribe", map[string]any{"session_id": fakeSessionID}); resp.Error != nil {
		t.Fatalf("subscribe: %+v", resp.Error)
	}

	s.dispatchTestOutput(fakeSessionID, 1, []byte("hello"))
	method, envelope, params := c.readNotification(t)
	if method != "session.output" {
		t.Fatalf("method = %q, want session.output", method)
	}
	if envelope == 0 {
		t.Error("session.output carries no envelope seq; §6 numbers every notification")
	}

	// The two numbers are independent. The payload's counts bytes within this terminal
	// (§5.11); the envelope's counts notifications across the daemon.
	var payload struct {
		Seq uint64 `json:"seq"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		t.Fatalf("params: %v", err)
	}
	if payload.Seq != 1 {
		t.Errorf("params.seq = %d, want the session's own 1", payload.Seq)
	}
}

// TestLayoutUpdatedIsSequencedToo guards the tree notifications built by `toNotification`:
// their params are a bare `Layout`, with no envelope of their own, so a `broadcast` that
// dropped the number on the way would leave them at zero.
//
// It covers the `broadcast` path only. The notification the *subscription* goroutine emits by
// itself, `session.unsubscribed`, is covered by
// TestUnsubscribedFromTheSubscriptionGoroutineCarriesSeq_REQ_API_002.
func TestLayoutUpdatedIsSequencedToo(t *testing.T) {
	t.Parallel()

	s := testServerWithTree(t, &fakeTree{tree: sampleTree()})
	c := dialTree(t, s)

	method, params := toNotification(wsports.LayoutUpdated{
		Layout: wsdomain.Layout{WorkspaceID: "w1", TabID: "w1:t1"},
	})
	s.broadcast(method, params, s.nextSeq())

	if _, seq, _ := c.readNotification(t); seq == 0 {
		t.Error("layout.updated arrived with seq 0")
	}
}

// TestSeqIsAssignedAtDispatchNotAtDelivery_REQ_API_002 pins decision 5 of the
// `2026-09-notification-sequencing` delta: the counter is monotonic in *assignment*, not in
// what a connection sees arrive.
//
// The mechanism is the whole finding. `dispatchOutput` takes a number and hands the chunk to
// the per-client queue of §8, where it waits; a tree notification dispatched a moment later
// takes the next number and is written to the socket directly, overtaking it. A connection
// subscribed to a session therefore sees a higher `seq` before a lower one.
//
// This is asserted on the queue rather than on the socket because the overtaking is a race by
// construction — which is exactly why the protocol cannot promise ordering. What is
// deterministic, and what the client's rule depends on, is that a chunk carries the number it
// was dispatched under and not the one current when it is finally written.
func TestSeqIsAssignedAtDispatchNotAtDelivery_REQ_API_002(t *testing.T) {
	t.Parallel()

	s := testServerWithTree(t, &fakeTree{tree: sampleTree()})
	sub := &subscription{
		sessionID: fakeSessionID,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	// An output chunk is dispatched: it takes a number and goes into the queue unwritten.
	queued := s.nextSeq()
	sub.enqueue(1, queued, []byte("output dispatched first"))

	// A tree notification dispatched afterwards takes a higher number and, on the direct
	// path, reaches the client while the chunk above is still waiting.
	later := s.nextSeq()
	if later <= queued {
		t.Fatalf("the counter went %d then %d; it must not repeat", queued, later)
	}

	// Only now is the chunk drained, and it still carries its own number.
	batch, _, envelope := sub.take()
	if len(batch) == 0 {
		t.Fatal("the queued chunk was not drained")
	}
	if envelope != queued {
		t.Errorf("the chunk was written carrying seq %d, want the %d it was dispatched under: "+
			"renumbering on delivery would make the snapshot's seq mean nothing", envelope, queued)
	}
	if envelope >= later {
		t.Errorf("seq %d reached the client after %d had already been written; a client that "+
			"discarded anything not greater than the last one it saw would drop this chunk",
			envelope, later)
	}
}

// TestUnsubscribedFromTheSubscriptionGoroutineCarriesSeq_REQ_API_002 covers the one
// notification the daemon emits from a subscription's own goroutine rather than from a
// dispatch or a handler.
//
// §6 numbers every notification, and `session.unsubscribed` is the one that reports loss —
// the single message a client must never mistake for a gap. It is emitted from `run` after
// the queue overflows, a path no other test reads, so a number missing there would be
// invisible.
func TestUnsubscribedFromTheSubscriptionGoroutineCarriesSeq_REQ_API_002(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := c.call(2, "session.subscribe", map[string]any{"session_id": fakeSessionID}); resp.Error != nil {
		t.Fatalf("subscribe: %+v", resp.Error)
	}

	// Overflow the queue without reading it: past ClientQueueBytes the daemon drops the
	// subscription and says so.
	chunk := make([]byte, 1<<20)
	for seq := uint64(1); seq <= (ClientQueueBytes/uint64(len(chunk)))+2; seq++ {
		s.dispatchTestOutput(fakeSessionID, seq, chunk)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no session.unsubscribed arrived; the queue limit did not drop the subscription")
		}
		method, seq, _ := c.readNotification(t)
		if method != "session.unsubscribed" {
			continue
		}
		if seq == 0 {
			t.Error("session.unsubscribed carries seq 0; §6 numbers every notification, and " +
				"this is the one that reports loss")
		}
		return
	}
}
