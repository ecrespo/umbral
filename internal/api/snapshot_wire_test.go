package api

import (
	"encoding/json"
	"testing"

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
// present on every notification, strictly increasing, and the same number for two clients.
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
			t.Errorf("seq went %d then %d; §6 says it increases", firstSeqs[i-1], seq)
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

// TestLayoutUpdatedIsSequencedToo guards against the counter being threaded through some
// notification paths and not others: the subscription goroutine emits on its own, and a
// number assigned only on the dispatch path would leave those at zero.
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
