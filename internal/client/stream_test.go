package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

// TestStreamDeliversNotificationsWhileACallIsInFlight is the whole reason this type exists
// beside Client. The plain client skips notifications while it waits for a reply; a TUI
// cannot, because output arrives when nothing was asked for. Both have to work at once.
func TestStreamDeliversNotificationsWhileACallIsInFlight(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		if method == "system.hello" {
			_ = enc.Encode(helloReply(id))
			return
		}
		// Output first, then the reply: the order a busy session produces.
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "method": "session.output",
			"params": map[string]any{"session_id": "ses_x", "seq": 4, "data_b64": "aGk="},
		})
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"seq": 3},
		})
	})

	s := dialStream(t, socket)

	var result struct {
		Seq uint64 `json:"seq"`
	}
	if err := s.Call(t.Context(), "session.subscribe", map[string]any{"session_id": "ses_x"}, &result); err != nil {
		t.Fatalf("session.subscribe: %v", err)
	}
	if result.Seq != 3 {
		t.Errorf("seq = %d, want 3: the call took a notification for its reply", result.Seq)
	}

	n := awaitNotification(t, s)
	if n.Method != "session.output" {
		t.Errorf("method = %q, want session.output", n.Method)
	}
	var params struct {
		SessionID string `json:"session_id"`
		Seq       uint64 `json:"seq"`
		DataB64   string `json:"data_b64"`
	}
	if err := n.Decode(&params); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if params.Seq != 4 || params.SessionID != "ses_x" {
		t.Errorf("params = %+v, want the seq 4 chunk of ses_x", params)
	}
}

// TestStreamEndsOnEOFRatherThanHanging: when the daemon goes away the TUI has to find out,
// so it can show a disconnected state instead of a frozen screen. The channel closing is
// how it finds out, and Err says why.
func TestStreamEndsOnEOFRatherThanHanging(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		_ = enc.Encode(helloReply(id))
	})
	s := dialStream(t, socket)

	// Hanging up from this side is the same event for the reader as the daemon dying.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-s.Notifications():
		if ok {
			t.Error("a notification arrived after the connection closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the notification channel never closed; the TUI would freeze rather than reconnect")
	}
	if err := s.Err(); !errors.Is(err, io.EOF) {
		t.Errorf("Err = %v, want io.EOF so the caller can tell a hang-up from a protocol error", err)
	}
}

// TestStreamCallFailsOnceTheStreamEnded stops the TUI waiting forever on a method it sent
// to a daemon that is already gone.
func TestStreamCallFailsOnceTheStreamEnded(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		_ = enc.Encode(helloReply(id))
	})
	s := dialStream(t, socket)
	_ = s.Close()

	// Wait for the reader to notice, so the test exercises the ended-stream path rather
	// than the closed-connection one.
	<-s.Notifications()

	err := s.Call(t.Context(), "session.list", nil, nil)
	if err == nil {
		t.Fatal("a call on an ended stream succeeded")
	}
}

// TestStreamOverflowsInsteadOfGrowing covers the caller that stops reading. Growing
// without bound would trade a slow TUI for an out-of-memory one; the recovery is a fresh
// snapshot, which is what the daemon does on its own side of the same problem.
func TestStreamOverflowsInsteadOfGrowing(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		if method == "system.hello" {
			_ = enc.Encode(helloReply(id))
			return
		}
		for i := range NotificationBuffer * 2 {
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session.output",
				"params": map[string]any{"session_id": "ses_x", "seq": i, "data_b64": "eA=="},
			})
		}
	})

	s := dialStream(t, socket)
	// Ask for something so the fake starts pushing, and deliberately never drain.
	_ = s.Call(t.Context(), "session.subscribe", map[string]any{"session_id": "ses_x"}, nil)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("the stream neither overflowed nor ended while nobody was reading")
		default:
		}
		if err := s.Err(); err != nil {
			if !errors.Is(err, ErrStreamOverflow) {
				t.Errorf("Err = %v, want ErrStreamOverflow", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func dialStream(t *testing.T, socket string) *Stream {
	t.Helper()
	c, err := Dial(context.Background(), socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	s := NewStream(c)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func awaitNotification(t *testing.T, s *Stream) Notification {
	t.Helper()
	select {
	case n, ok := <-s.Notifications():
		if !ok {
			t.Fatalf("the stream ended before a notification arrived: %v", s.Err())
		}
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("no notification arrived")
		return Notification{}
	}
}

// pushThenReply is a fake daemon that answers the hello, then answers the next request with
// n numbered notifications followed by its reply.
func pushThenReply(t *testing.T, n int) string {
	t.Helper()
	return fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		if method == "system.hello" {
			_ = enc.Encode(helloReply(id))
			return
		}
		for i := range n {
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session.output",
				"params": map[string]any{"session_id": "ses_x", "seq": i, "data_b64": "eA=="},
			})
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
	})
}

func dialUnbounded(t *testing.T, socket string) *Stream {
	t.Helper()
	c, err := Dial(context.Background(), socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	s := NewUnboundedStream(c)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestAnUnboundedStreamKeepsEveryNotification_REQ_CLI_001: `umb ai` must print every delta of
// an answer, however far its reader falls behind. With twice the buffer pushed and nothing
// read, the reply behind them still arrives — the reader never stopped reading the socket —
// and then every notification comes out, in order. The default stream overflows here.
func TestAnUnboundedStreamKeepsEveryNotification_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	const n = NotificationBuffer * 2
	s := dialUnbounded(t, pushThenReply(t, n))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.Call(ctx, "session.subscribe", map[string]any{"session_id": "ses_x"}, nil); err != nil {
		t.Fatalf("the reply behind %d unread notifications: %v", n, err)
	}
	for i := range n {
		var p struct {
			Seq int `json:"seq"`
		}
		select {
		case got := <-s.Notifications():
			if err := got.Decode(&p); err != nil || p.Seq != i {
				t.Fatalf("notification %d has seq %d (%v): one was lost or reordered", i, p.Seq, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("notification %d never arrived; Err = %v", i, s.Err())
		}
	}
}

// TestClosingAnUnboundedStreamFreesItsForwarder: a forwarder waiting on a caller that stopped
// reading must end when the caller closes the stream, or it would outlive it.
func TestClosingAnUnboundedStreamFreesItsForwarder(t *testing.T) {
	t.Parallel()
	s := dialUnbounded(t, pushThenReply(t, NotificationBuffer*2))
	if err := s.Call(t.Context(), "session.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	// Nothing reads the channel: only Close can free the forwarder held on the full buffer.
	select {
	case <-s.forwarded:
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarder is still held on a full buffer after Close")
	}
}
