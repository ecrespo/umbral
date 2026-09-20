package api

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"
)

// collectNotifications reads notifications whose method matches, returning once the stream
// has gone quiet or `until` has elapsed.
//
// **A read deadline poisons a json.Decoder permanently.** `Decode` stores the first error it
// hits and returns that same error to every later call, instantly, even once the message has
// arrived — measured at about a microsecond per call. So a loop that treats a timeout as
// "nothing yet, try again" does not wait: it burns a core until its budget runs out and then
// reports that nothing came. This helper did exactly that, and raising its budget from two
// seconds to fifteen turned a short spin into a long one; `TestSubscribeSnapshotBeforeLive`
// failed under `-race` for that reason and no other.
//
// So a timeout ends the collection rather than continuing it, and the waits are sized for
// what each one is for: the first read waits the whole budget, because on a loaded machine
// the first notification is the one that can take seconds to arrive, and every read after it
// waits only `idleGap`, because by then the stream is flowing and a gap means the end.
func (c *client) collectNotifications(method string, until time.Duration) []map[string]any {
	c.t.Helper()
	return c.collectNotificationsUntil(method, until, nil)
}

// collectNotificationsUntil collects until `enough` is satisfied, then to the first gap.
//
// The predicate is what a test that knows its own expectation should use. "Stop at the first
// quiet moment" is right for a test that will take whatever arrives, and wrong for one that
// sent a known quantity: under `-race` a burst can stall mid-stream for longer than a gap,
// and the collector would return half of it and call the stream finished.
// TestBatchingCoalescesABurst failed that way — 78 of 200 bytes, reported as lost output —
// which is a false accusation against the daemon, the worst kind of flake.
//
// So while `enough` is unmet every read waits the whole remaining budget. Once it is met the
// reads shorten to `idleGap`, to sweep up anything extra that a test may want to assert is
// *not* there.
func (c *client) collectNotificationsUntil(
	method string, until time.Duration, enough func([]map[string]any) bool,
) []map[string]any {
	c.t.Helper()

	// Long enough that a batch arriving in pieces is not cut in half, short enough that a
	// test which collects nothing more does not pay for it.
	const idleGap = 300 * time.Millisecond

	var out []map[string]any
	deadline := time.Now().Add(until)
	for {
		wait := time.Until(deadline)
		if enough != nil && enough(out) {
			wait = idleGap
		} else if len(out) > 0 && enough == nil {
			wait = idleGap
		}
		if wait <= 0 {
			return out
		}
		if err := c.conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
			return out
		}

		var msg struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := c.dec.Decode(&msg); err != nil {
			// Whether this is a timeout or a closed connection, the decoder is finished:
			// nothing further can be read from it. Returning is the only honest move.
			return out
		}
		if msg.Method == method {
			out = append(out, msg.Params)
		}
	}
}

// TestSubscribeSnapshotBeforeLive_REQ_TERM_004 is the ordering promise: a client that
// attaches to a running session gets the screen before any live chunk, and the two do not
// overlap.
//
// The sequence number is what makes that checkable. The snapshot is current as of `seq`,
// and the first notification must carry a higher one: a client that received a chunk it
// already had on screen would print it twice.
func TestSubscribeSnapshotBeforeLive_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	// Output already on screen before anyone subscribes.
	sessions.setSnapshot([]byte("\x1b[Hscrollback and screen"), 7, 3, 12)

	// The ordering is asserted where it happens rather than inferred from a race that
	// may go either way: at the moment the snapshot is taken, the subscription must
	// already exist. Output arriving in that window is then queued instead of lost, and
	// the snapshot's sequence number discards whatever it already contains.
	var subscribedAtSnapshotTime bool
	sessions.duringSnapshot(func() {
		subscribedAtSnapshotTime = s.hasSubscription(fakeSessionID)
		s.dispatchTestOutput(fakeSessionID, 8, []byte("arrived mid-snapshot"))
	})

	resp := c.call(2, "session.subscribe", map[string]any{"session_id": fakeSessionID})
	if resp.Error != nil {
		t.Fatalf("session.subscribe: %+v", resp.Error)
	}

	var result struct {
		Snapshot struct {
			Format  string `json:"format"`
			DataB64 string `json:"data_b64"`
			Cursor  struct {
				X uint16 `json:"x"`
				Y uint16 `json:"y"`
			} `json:"cursor"`
		} `json:"snapshot"`
		Seq uint64 `json:"seq"`
	}
	decodeResult(t, resp, &result)

	if !subscribedAtSnapshotTime {
		t.Error("the subscription was registered after the snapshot was taken;" +
			" output arriving in between is lost with nothing to notice the loss")
	}

	if result.Snapshot.Format != "vt" {
		t.Errorf("snapshot format = %q, want vt", result.Snapshot.Format)
	}
	data, err := base64.StdEncoding.DecodeString(result.Snapshot.DataB64)
	if err != nil {
		t.Fatalf("snapshot is not base64: %v", err)
	}
	if !strings.Contains(string(data), "scrollback and screen") {
		t.Errorf("snapshot does not carry the screen: %q", data)
	}
	if result.Seq != 7 {
		t.Errorf("seq = %d, want the snapshot's sequence number 7", result.Seq)
	}
	if result.Snapshot.Cursor.X != 3 || result.Snapshot.Cursor.Y != 12 {
		t.Errorf("cursor = (%d,%d), want (3,12)", result.Snapshot.Cursor.X, result.Snapshot.Cursor.Y)
	}

	// A chunk the snapshot already contains must not be replayed.
	s.dispatchTestOutput(fakeSessionID, 7, []byte("already on screen"))
	s.dispatchTestOutput(fakeSessionID, 9, []byte("live output"))

	notifications := c.collectNotificationsUntil("session.output", 15*time.Second,
		func(got []map[string]any) bool { return bytes.Contains(payload(got), []byte("live output")) })
	if len(notifications) == 0 {
		t.Fatal("no session.output notification arrived after subscribing")
	}

	var received []byte
	for _, n := range notifications {
		chunk, err := base64.StdEncoding.DecodeString(n["data_b64"].(string))
		if err != nil {
			t.Fatalf("notification data is not base64: %v", err)
		}
		received = append(received, chunk...)

		if seq, ok := n["seq"].(float64); ok && uint64(seq) <= result.Seq {
			t.Errorf("a notification carried seq %v, which the snapshot already contained", seq)
		}
	}
	if strings.Contains(string(received), "already on screen") {
		t.Error("a chunk the snapshot already contained was sent again; the client would print it twice")
	}
	if !strings.Contains(string(received), "live output") {
		t.Errorf("the live chunk never arrived; got %q", received)
	}
	if !strings.Contains(string(received), "arrived mid-snapshot") {
		t.Errorf("the chunk published while the snapshot was being taken was lost; got %q", received)
	}
}

// TestSessionSurvivesNoClients_REQ_TERM_003 checks the daemon's side of durability: output
// published while nobody is subscribed is neither queued forever nor an error, and a client
// that subscribes afterwards is served normally.
func TestSessionSurvivesNoClients_REQ_TERM_003(t *testing.T) {
	t.Parallel()

	sessions := newFakeSessions()
	s := testServerWithSessions(t, sessions)

	// Nobody is connected at all. This must not block, panic or accumulate.
	for seq := uint64(1); seq <= 100; seq++ {
		s.dispatchTestOutput(fakeSessionID, seq, []byte("output with no audience\n"))
	}

	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	sessions.setSnapshot([]byte("the screen as it stands"), 100, 0, 0)

	resp := c.call(2, "session.subscribe", map[string]any{"session_id": fakeSessionID})
	if resp.Error != nil {
		t.Fatalf("subscribing after the fact failed: %+v", resp.Error)
	}

	s.dispatchTestOutput(fakeSessionID, 101, []byte("after attaching"))
	notifications := c.collectNotificationsUntil("session.output", 15*time.Second,
		func(got []map[string]any) bool { return bytes.Contains(payload(got), []byte("after attaching")) })
	if len(notifications) == 0 {
		t.Fatal("a client that attached to a session with history received nothing")
	}
	if !bytes.Contains(payload(notifications), []byte("after attaching")) {
		t.Errorf("the chunk sent after attaching never arrived; got %q", payload(notifications))
	}
}

// TestUnsubscribeStopsDelivery checks that unsubscribing actually stops the stream, and
// that unsubscribing twice is not an error.
func TestUnsubscribeStopsDelivery(t *testing.T) {
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
	if resp := c.call(3, "session.unsubscribe", map[string]any{"session_id": fakeSessionID}); resp.Error != nil {
		t.Fatalf("unsubscribe: %+v", resp.Error)
	}
	if resp := c.call(4, "session.unsubscribe", map[string]any{"session_id": fakeSessionID}); resp.Error != nil {
		t.Errorf("unsubscribing twice was an error: %+v", resp.Error)
	}

	s.dispatchTestOutput(fakeSessionID, 1, []byte("must not arrive"))
	if got := c.collectNotifications("session.output", time.Second); len(got) != 0 {
		t.Errorf("received %d notifications after unsubscribing", len(got))
	}
}

// TestSlowClientIsDropped covers the queue limit of API Spec §8: past 8 MiB the daemon
// drops the subscription rather than grow without bound, and tells the client so it can
// re-subscribe and get a fresh snapshot.
func TestSlowClientIsDropped(t *testing.T) {
	t.Parallel()

	sub := &subscription{
		sessionID: fakeSessionID,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	chunk := make([]byte, 64<<10)
	var seq uint64
	for sub.queued+len(chunk) <= ClientQueueBytes {
		seq++
		sub.enqueue(seq, seq, chunk)
	}
	if sub.overflow {
		t.Fatal("the subscription overflowed before reaching the limit")
	}
	queuedBefore := sub.queued

	seq++
	sub.enqueue(seq, seq, chunk)

	if !sub.overflow {
		t.Fatalf("the subscription accepted %d bytes, past the %d limit", queuedBefore+len(chunk), ClientQueueBytes)
	}
	if sub.queued != 0 {
		t.Errorf("the dropped subscription still holds %d bytes; they must be released", sub.queued)
	}

	// Once overflowed it accepts nothing more: the client is getting a new snapshot.
	seq++
	sub.enqueue(seq, seq, chunk)
	if sub.queued != 0 {
		t.Errorf("an overflowed subscription queued %d more bytes", sub.queued)
	}
}

// TestRebaseKeepsWhatTheSnapshotDoesNotContain_REQ_TERM_004 is the deterministic half of
// the gapless promise. `session.subscribe` registers the subscription before it asks for the
// snapshot, precisely so that output produced in that window is queued; the seq the snapshot
// turns out to be current as of is only known afterwards.
//
// Applying it must drop exactly the prefix the snapshot already shows and keep the rest.
// Replacing the subscription instead, which is what the handler used to do, dropped the whole
// queue and lost the chunk whenever the bus delivered it before the snapshot returned.
func TestRebaseKeepsWhatTheSnapshotDoesNotContain_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	sub := &subscription{
		sessionID: fakeSessionID,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	sub.enqueue(5, 5, []byte("on the snapshot"))
	sub.enqueue(7, 7, []byte("also on the snapshot"))
	sub.enqueue(9, 9, []byte("after the snapshot"))

	sub.rebase(7)

	out, _, _ := sub.take()
	if got := string(out); got != "after the snapshot" {
		t.Errorf("after rebasing to seq 7 the queue held %q, want only the seq 9 chunk", got)
	}
	if sub.queued != 0 {
		t.Errorf("the queue still accounts %d bytes after being drained", sub.queued)
	}
	if sub.lastSeq != 9 {
		t.Errorf("lastSeq = %d, want 9: rebasing must not lower the floor already reached", sub.lastSeq)
	}

	// Rebasing past everything queued leaves nothing, and a later chunk still arrives.
	sub.enqueue(11, 11, []byte("newer"))
	sub.rebase(20)
	if out, _, _ := sub.take(); len(out) != 0 {
		t.Errorf("rebasing past the queue left %q behind", out)
	}
	sub.enqueue(21, 21, []byte("newest"))
	if out, _, _ := sub.take(); string(out) != "newest" {
		t.Errorf("a chunk above the new floor was dropped; got %q", out)
	}
}

// TestBatchingCoalescesABurst checks the other half of API Spec §8: a burst of small chunks
// arrives as few notifications, not as one per chunk.
func TestBatchingCoalescesABurst(t *testing.T) {
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

	const chunks = 200
	for seq := uint64(1); seq <= chunks; seq++ {
		s.dispatchTestOutput(fakeSessionID, seq, []byte("x"))
	}

	notifications := c.collectNotificationsUntil("session.output", 15*time.Second,
		func(got []map[string]any) bool { return payloadBytes(got) >= chunks })
	if len(notifications) == 0 {
		t.Fatal("the burst produced no notifications")
	}
	if len(notifications) >= chunks {
		t.Errorf("%d chunks produced %d notifications; they were not coalesced", chunks, len(notifications))
	}

	var total int
	for _, n := range notifications {
		data, err := base64.StdEncoding.DecodeString(n["data_b64"].(string))
		if err != nil {
			t.Fatalf("notification data is not base64: %v", err)
		}
		total += len(data)
	}
	if total != chunks {
		t.Errorf("the batches carried %d bytes, want the %d that were sent: coalescing must not lose output",
			total, chunks)
	}
}

// TestNotificationSeqIsMonotonic guards the property a client uses to detect a gap.
func TestNotificationSeqIsMonotonic(t *testing.T) {
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

	for seq := uint64(1); seq <= 50; seq++ {
		s.dispatchTestOutput(fakeSessionID, seq, []byte("line\n"))
		time.Sleep(time.Millisecond)
	}

	notifications := c.collectNotificationsUntil("session.output", 15*time.Second,
		func(got []map[string]any) bool { return payloadBytes(got) >= 50*len("line\n") })
	seqs := make([]float64, 0, len(notifications))
	for _, n := range notifications {
		seqs = append(seqs, n["seq"].(float64))
	}
	if !slices.IsSorted(seqs) {
		t.Errorf("sequence numbers went backwards: %v", seqs)
	}
}

// TestNoChunkWaitsLongerThanTheBatchInterval pins the ceiling API Spec §8 sets. The writer
// sends immediately rather than waiting for a batch to fill, so the measured wait is far
// below it; this test is what would notice if someone reintroduced a fixed delay.
//
// It measures the median of many lone chunks rather than timing a single one. The quantity
// under test, 4 ms, is the same order as the scheduling noise of one socket round-trip under
// `-race`, so a single sample says nothing: it failed on roughly two runs in three at 5-6 ms
// while the daemon was flushing immediately. A reintroduced fixed timer would delay *every*
// chunk, which moves the median and not just the tail, so the test keeps its teeth.
func TestNoChunkWaitsLongerThanTheBatchInterval(t *testing.T) {
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

	// Each chunk goes out alone, with nothing following it: the case a fixed batch timer
	// punishes. The gap between them is what keeps them from coalescing into one batch.
	const samples = 25
	waits := make([]time.Duration, 0, samples)
	for seq := uint64(1); seq <= samples; seq++ {
		start := time.Now()
		s.dispatchTestOutput(fakeSessionID, seq, []byte("alone"))
		if !c.awaitOutputT(t, 5*time.Second) {
			t.Fatalf("the lone chunk %d never arrived", seq)
		}
		waits = append(waits, time.Since(start))
	}

	slices.Sort(waits)
	median := waits[len(waits)/2]
	t.Logf("a lone chunk waited %v at the median (min %v, max %v) against a %v ceiling",
		median, waits[0], waits[len(waits)-1], BatchInterval)

	if median > BatchInterval {
		t.Errorf("a lone chunk waited %v at the median, longer than the %v ceiling in API Spec §8",
			median, BatchInterval)
	}
}

// TestABatchNeverSplitsAChunk_REQ_TERM_004 pins the rule that keeps a long burst intact.
//
// A notification reports the session seq of the last chunk it carries, and the client
// discards anything at or below the seq it last applied (§5.11, and `internal/tui`'s screen
// writer does exactly that). So a batch that emitted half a chunk under seq N and the rest
// under seq N again would have the remainder thrown away as a duplicate: bytes gone from the
// middle of the screen, with nothing reporting a loss. `take` therefore stops on a chunk
// boundary, and the numbers it reports are strictly increasing.
//
// The sizes matter. The PTY reads 32 KiB, the same as BatchBytes, so a batch that already
// holds anything at all cannot fit a full chunk — this is the ordinary path under load, not
// an edge case.
func TestABatchNeverSplitsAChunk_REQ_TERM_004(t *testing.T) {
	t.Parallel()

	sub := &subscription{
		sessionID: fakeSessionID,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	// Three chunks that cannot be coalesced into one batch without splitting one.
	const chunks = 3
	for seq := uint64(1); seq <= chunks; seq++ {
		data := make([]byte, (BatchBytes*2)/3)
		for i := range data {
			data[i] = byte('a' + seq - 1)
		}
		sub.enqueue(seq, 100+seq, data)
	}

	var (
		assembled []byte
		seqs      []uint64
		envelopes []uint64
	)
	for {
		batch, seq, envelope := sub.take()
		if len(batch) == 0 {
			break
		}
		assembled = append(assembled, batch...)
		seqs = append(seqs, seq)
		envelopes = append(envelopes, envelope)
	}

	if len(seqs) < 2 {
		t.Fatalf("the queue came out in %d batch(es); this test needs it split to mean anything", len(seqs))
	}

	// Every byte, in order, and no chunk cut in half: each batch is a whole number of
	// chunks, so every run of identical bytes is exactly one chunk long.
	want := make([]byte, 0, chunks*((BatchBytes*2)/3))
	for seq := range uint64(chunks) {
		want = append(want, slices.Repeat([]byte{byte('a' + seq)}, (BatchBytes*2)/3)...)
	}
	if !slices.Equal(assembled, want) {
		t.Errorf("the reassembled stream is %d bytes, want %d", len(assembled), len(want))
	}

	for i, seq := range seqs {
		if i > 0 && seq <= seqs[i-1] {
			t.Errorf("batch %d reports seq %d after %d; the client drops anything not greater "+
				"than the last one it applied, so those bytes would vanish", i, seq, seqs[i-1])
		}
		if i > 0 && envelopes[i] <= envelopes[i-1] {
			t.Errorf("batch %d reports envelope %d after %d", i, envelopes[i], envelopes[i-1])
		}
	}
	if last := seqs[len(seqs)-1]; last != chunks {
		t.Errorf("the final batch reports seq %d, want %d: it carries the last chunk", last, chunks)
	}
}

// TestTheCollectorWaitsForASlowFirstNotification guards the helper every fan-out test
// depends on.
//
// A read deadline poisons a `json.Decoder` for good, so the obvious shape — a short deadline
// in a loop, `continue` on timeout — does not retry. It spins at roughly a microsecond a turn
// and then reports an empty stream, which reads as "the daemon sent nothing" when what
// happened is "the helper stopped listening". That is how `TestSubscribeSnapshotBeforeLive`
// failed under load, and the bug was invisible because on an idle machine the first
// notification always arrived inside the first window.
//
// Here the output is deliberately delayed past the idle gap. The old shape returns nothing;
// this one waits.
func TestTheCollectorWaitsForASlowFirstNotification(t *testing.T) {
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

	// Well past the 300 ms gap a flowing stream is measured by, and well inside the budget.
	const delay = 1500 * time.Millisecond
	go func() {
		time.Sleep(delay)
		s.dispatchTestOutput(fakeSessionID, 1, []byte("late but not lost"))
	}()

	start := time.Now()
	notifications := c.collectNotifications("session.output", 10*time.Second)
	elapsed := time.Since(start)

	if len(notifications) == 0 {
		t.Fatalf("the collector gave up after %v on output sent at %v; a slow first "+
			"notification must not read as an empty stream", elapsed, delay)
	}
	if elapsed < delay {
		t.Errorf("collected after %v, before the output was even sent at %v", elapsed, delay)
	}
}

// payload concatenates the bytes a set of session.output notifications carried.
func payload(notifications []map[string]any) []byte {
	var out []byte
	for _, n := range notifications {
		encoded, ok := n["data_b64"].(string)
		if !ok {
			continue
		}
		chunk, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		out = append(out, chunk...)
	}
	return out
}

func payloadBytes(notifications []map[string]any) int { return len(payload(notifications)) }
