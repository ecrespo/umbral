package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Notification is one message the daemon pushed, with its parameters still encoded so the
// caller decodes only what it cares about (API Spec §6).
type Notification struct {
	Method string
	// Seq is the envelope counter of API Spec §1 and §6: one daemon run, shared by every
	// connection. A client that keeps its own cache discards the notifications whose Seq is
	// not greater than the one `session.snapshot` reported.
	//
	// It is not `session.output`'s `seq`, which lives in Params and counts bytes within one
	// terminal (§5.11). The two are different numbers with the same name, which is why this
	// one is named on the envelope and that one stays in the payload.
	Seq    uint64
	Params json.RawMessage
}

// Decode unmarshals the parameters into v.
func (n Notification) Decode(v any) error {
	if err := json.Unmarshal(n.Params, v); err != nil {
		return fmt.Errorf("client: decode %s: %w", n.Method, err)
	}
	return nil
}

// NotificationBuffer is how many notifications a Stream holds for a caller that has not
// read them yet.
//
// It is generous because `session.output` arrives in batches of up to 32 KiB every 4 ms
// (API Spec §8) and a TUI redrawing a frame can be a few milliseconds behind. It is
// bounded because a caller that stops reading entirely must not grow the client without
// limit; past it, the stream stops rather than blocks, which the caller learns from Err.
const NotificationBuffer = 1024

// ErrStreamOverflow reports that the caller fell too far behind and the stream gave up.
//
// It is the client-side twin of the daemon's `session.unsubscribed`: the recovery is the
// same, re-subscribe and take a fresh snapshot, because the screen is worth more than the
// backlog.
var ErrStreamOverflow = errors.New("client: the notification buffer overflowed")

// Stream is a connection that reads continuously, delivering notifications on a channel
// and still answering calls.
//
// The plain Client is request/response: `umb` never subscribes to anything, so its `call`
// simply skips notifications it happens to meet while waiting for a reply. A client that
// wants the stream — the TUI — cannot work that way, because output arrives when nothing
// was asked for. This type owns the reader instead, and routes each frame by whether it
// carries an id.
type Stream struct {
	c    *Client
	ch   chan Notification
	done chan struct{}

	replies chan *rpcResponse

	errMu sync.Mutex
	err   error
}

// NewStream takes ownership of a connected client and starts reading.
//
// The client must not be used directly afterwards: its Call would race the stream's
// reader for frames. Use the Stream's own Call.
func NewStream(c *Client) *Stream {
	s := &Stream{
		c:       c,
		ch:      make(chan Notification, NotificationBuffer),
		done:    make(chan struct{}),
		replies: make(chan *rpcResponse, 1),
	}
	go s.read()
	return s
}

// Notifications is the channel notifications arrive on. It is closed when the stream ends,
// and Err then says why.
func (s *Stream) Notifications() <-chan Notification { return s.ch }

// Err reports why the stream ended, or nil while it is running. io.EOF means the daemon
// closed the connection.
func (s *Stream) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Close ends the stream and releases the connection.
func (s *Stream) Close() error { return s.c.Close() }

// Call invokes a method and waits for its reply, while notifications keep flowing to the
// channel. Only one call may be in flight at a time, which is what the daemon's NDJSON
// stream allows anyway.
func (s *Stream) Call(ctx context.Context, method string, params, out any) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	if s.c.conn == nil {
		return errors.New("client: the connection is closed")
	}
	s.c.nextID++
	id := s.c.nextID

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := s.c.enc.Encode(req); err != nil {
		return fmt.Errorf("client: send %s: %w", method, err)
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("client: %s: %w", method, ctx.Err())
		case <-s.done:
			if err := s.Err(); err != nil {
				return fmt.Errorf("client: %s: the stream ended: %w", method, err)
			}
			return fmt.Errorf("client: %s: the stream ended", method)
		case resp := <-s.replies:
			// A reply to an earlier call that timed out is not this one's answer.
			if resp.ID == nil || *resp.ID != id {
				continue
			}
			if resp.Error != nil {
				return &Error{
					Code:       resp.Error.Code,
					Message:    resp.Error.Message,
					DomainCode: resp.Error.Data.DomainCode,
					Details:    resp.Error.Data.Details,
					TraceID:    resp.Error.Data.TraceID,
				}
			}
			if out == nil || len(resp.Result) == 0 {
				return nil
			}
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("client: decode the result of %s: %w", method, err)
			}
			return nil
		}
	}
}

// read is the single reader. Everything that arrives is either a reply, which goes to
// whoever is waiting, or a notification, which goes to the channel.
func (s *Stream) read() {
	defer close(s.ch)
	defer close(s.done)

	for {
		resp, err := s.c.readFrame()
		if err != nil {
			s.setErr(normalizeReadError(err))
			return
		}

		if resp.ID != nil {
			// A reply nobody is waiting for is dropped rather than queued: the caller
			// that sent it has already given up, and holding it would hand it to the
			// next call as if it were its own.
			select {
			case s.replies <- resp:
			default:
			}
			continue
		}
		if resp.Method == "" {
			continue
		}

		select {
		case s.ch <- Notification{Method: resp.Method, Seq: resp.Seq, Params: resp.Params}:
		default:
			// The caller is too far behind to catch up. Stopping is the honest outcome:
			// a client that silently dropped output would show a screen missing bytes it
			// could never ask for again, and the fix is a fresh snapshot.
			s.setErr(ErrStreamOverflow)
			return
		}
	}
}

func (s *Stream) setErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// normalizeReadError turns the many ways a closed socket presents itself into io.EOF, so
// a caller can tell "the daemon went away" from "the daemon said something wrong".
func normalizeReadError(err error) error {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return io.EOF
	case errors.Is(err, io.ErrUnexpectedEOF):
		return io.EOF
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// A stream has no deadline of its own, so a timeout here is one the caller set
		// and then cancelled; treat it as the connection ending.
		return io.EOF
	}
	return err
}
