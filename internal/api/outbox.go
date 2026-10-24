package api

import (
	"log/slog"
	"sync"
)

// ClientNotificationQueueBytes is how much of the control notifications broadcast to every
// client one connection may hold unwritten (API Spec §8). Past it the daemon closes the
// connection: a client that far behind has stopped reading, and the snapshot it takes on
// reconnecting replaces everything it missed.
const ClientNotificationQueueBytes = 8 << 20

// outbox is one connection's queue of framed control notifications, drained by that
// connection's own writer so the dispatcher never waits for a socket (REQ-API-006).
//
// It closes rather than drops. `session.output` can shed a subscription because the next
// snapshot re-sends the screen, but a lost `pane.closed` or `thread.turn_finished` leaves the
// client's cache different from the daemon's with nothing to say so.
type outbox struct {
	mu       sync.Mutex
	lines    [][]byte
	queued   int
	overflow bool
	wake     chan struct{}
}

func newOutbox() *outbox { return &outbox{wake: make(chan struct{}, 1)} }

// push queues one framed line. It reports false once the queue is over its limit, now or
// before, and then holds nothing: the connection is being closed. An empty queue always takes
// a line, so a notification the frame limit allows but the queue limit does not, possible
// with `[api] max_message_bytes` above 8 MiB, is written rather than taken for a stalled client.
func (q *outbox) push(line []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.overflow {
		return false
	}
	if len(q.lines) > 0 && q.queued+len(line) > ClientNotificationQueueBytes {
		q.overflow = true
		q.lines, q.queued = nil, 0
		return false
	}
	q.lines = append(q.lines, line)
	q.queued += len(line)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// pop takes the oldest line, if there is one and the queue has not overflowed.
func (q *outbox) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.overflow || len(q.lines) == 0 {
		return nil, false
	}
	line := q.lines[0]
	q.lines[0] = nil
	q.lines = q.lines[1:]
	q.queued -= len(line)
	return line, true
}

// enqueue frames a broadcast notification for this connection and queues it. A connection
// over the limit is closed here, on the dispatcher's goroutine: closing the socket is what
// frees a writer blocked on it.
func (c *conn) enqueue(method string, params any, seq uint64) {
	select {
	case <-c.closed:
		return
	default:
	}
	line, err := c.frame(notification{JSONRPC: jsonrpcVersion, Method: method, Seq: seq, Params: params})
	if err != nil {
		c.logger.Error("encode an outgoing message", slog.Any("error", err))
		return
	}
	if !c.out.push(line) {
		c.overflowOnce.Do(func() {
			c.logger.Warn("connection closed: the client fell more than the notification queue limit behind",
				slog.String("connection_id", c.connectionID),
				slog.Int("queue_bytes", ClientNotificationQueueBytes))
			_ = c.close()
		})
	}
}

// writeOutbox writes the connection's queued notifications, in order, until it closes.
func (c *conn) writeOutbox() {
	for {
		select {
		case <-c.closed:
			return
		case <-c.out.wake:
		}
		for {
			line, ok := c.out.pop()
			if !ok {
				break
			}
			c.writeLine(line)
		}
	}
}
