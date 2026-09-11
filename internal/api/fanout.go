package api

import (
	"encoding/base64"
	"log/slog"
	"sync"
	"time"
)

// Fan-out limits from API Spec §8.
const (
	// BatchInterval caps how long a chunk waits to be coalesced with the next one.
	// It is a ceiling, not a target: an idle subscription flushes immediately, because
	// REQ-TERM-006 budgets 5 ms p95 for the whole daemon and spending 4 ms of it waiting
	// for company that never arrives would leave almost nothing for the rest.
	BatchInterval = 4 * time.Millisecond
	// BatchBytes flushes early once this much output has accumulated.
	BatchBytes = 32 << 10
	// ClientQueueBytes is how much unsent output one subscription may hold. Beyond it the
	// daemon drops the subscription rather than grow without bound; the client
	// re-subscribes and gets a fresh snapshot.
	ClientQueueBytes = 8 << 20
)

// DropReasonSlowClient is the reason reported when a subscription is dropped for
// exceeding ClientQueueBytes.
const DropReasonSlowClient = "slow_client"

// subscription is one connection's stream of one session's output.
//
// Each has its own goroutine, so a client that stops reading its socket blocks only
// itself: the writer that feeds it never waits, it accounts bytes and eventually gives up
// on that subscription.
type subscription struct {
	sessionID string
	conn      *conn
	logger    *slog.Logger

	mu       sync.Mutex
	pending  [][]byte
	queued   int
	lastSeq  uint64
	closed   bool
	overflow bool

	wake   chan struct{}
	done   chan struct{}
	closer sync.Once
}

func newSubscription(c *conn, sessionID string, startSeq uint64) *subscription {
	s := &subscription{
		sessionID: sessionID,
		conn:      c,
		logger:    c.logger,
		lastSeq:   startSeq,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	go s.run()
	return s
}

// enqueue accepts one output chunk. It never blocks: the caller is the daemon's single
// dispatch goroutine, and one unresponsive client must not stall every other one.
//
// A chunk whose sequence number the subscription already has is dropped. That is what
// makes `session.subscribe` gapless without being duplicative: the snapshot is current as
// of some seq, and anything at or below it is already on the client's screen.
func (s *subscription) enqueue(seq uint64, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.overflow {
		return
	}
	if seq <= s.lastSeq {
		return
	}
	s.lastSeq = seq

	if s.queued+len(data) > ClientQueueBytes {
		// API Spec §8: past the queue limit the daemon drops the subscription. Keeping
		// the bytes would trade a slow client for an out-of-memory daemon.
		s.overflow = true
		s.pending = nil
		s.queued = 0
		s.signal()
		return
	}

	s.pending = append(s.pending, data)
	s.queued += len(data)
	s.signal()
}

// signal wakes the writer without blocking if it is already awake.
func (s *subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// run is the subscription's writer. It sends whatever is queued as soon as it can and
// coalesces only what accumulates while the previous write is in flight.
//
// Waiting for a batch to fill would be the obvious reading of API Spec §8, and it is the
// wrong one: 4 ms is a ceiling on how long a chunk may wait, not a target. Measured, a
// fixed 4 ms wait puts the daemon's added latency at p95 4.2 ms against REQ-TERM-006's
// 5 ms budget, leaving nothing for the rest of the path. Sending at once puts it at 60 µs,
// and batching still happens exactly when it is needed: under load the writer is the slow
// part, so the chunks that pile up behind it go out together.
func (s *subscription) run() {
	defer close(s.done)

	for {
		select {
		case <-s.wake:
		case <-s.conn.closedCh():
			return
		}

		if s.takeOverflow() {
			s.conn.notify("session.unsubscribed", map[string]any{
				fieldSessionID: s.sessionID,
				"reason":       DropReasonSlowClient,
			})
			s.logger.Warn("subscription dropped: the client fell more than the queue limit behind",
				slog.String("session_id", s.sessionID), slog.Int("queue_bytes", ClientQueueBytes))
			return
		}

		for {
			batch, seq := s.take()
			if len(batch) == 0 {
				break
			}
			s.conn.notify("session.output", map[string]any{
				fieldSessionID: s.sessionID,
				"seq":          seq,
				fieldDataB64:   base64.StdEncoding.EncodeToString(batch),
			})
		}

		if s.isClosed() {
			return
		}
	}
}

// take removes up to BatchBytes of queued output. A longer queue is split across several
// notifications rather than sent as one oversized message, which is the other half of
// API Spec §8 and keeps each message well inside the 4 MiB framing limit.
func (s *subscription) take() ([]byte, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pending) == 0 {
		return nil, s.lastSeq
	}

	out := make([]byte, 0, min(s.queued, BatchBytes))
	for len(s.pending) > 0 && len(out) < BatchBytes {
		chunk := s.pending[0]
		room := BatchBytes - len(out)
		if len(chunk) > room {
			out = append(out, chunk[:room]...)
			s.pending[0] = chunk[room:]
			s.queued -= room
			break
		}
		out = append(out, chunk...)
		s.pending = s.pending[1:]
		s.queued -= len(chunk)
	}

	// More is waiting, so wake ourselves rather than rely on the next enqueue.
	if len(s.pending) > 0 {
		s.signal()
	}
	return out, s.lastSeq
}

func (s *subscription) takeOverflow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overflow
}

func (s *subscription) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// close stops the subscription. It is safe to call more than once.
func (s *subscription) close() {
	s.mu.Lock()
	s.closed = true
	s.pending = nil
	s.queued = 0
	s.mu.Unlock()

	s.closer.Do(func() { s.signal() })
}
