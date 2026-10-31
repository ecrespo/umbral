package api

import (
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// relayFixture is a server-side connection with one link to nas over a pipe.
func relayFixture(t *testing.T) (*conn, *relayLink, net.Conn) {
	t.Helper()
	s := testServerWithConfig(t, Config{Remotes: fakeRemotes{}})
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { _ = clientSide.Close() })
	c := s.newConn(serverSide)
	hostSide, upstream := net.Pipe()
	t.Cleanup(func() { _ = hostSide.Close() })
	l := &relayLink{
		host: "nas", conn: c, nc: upstream, nextID: 1,
		pending: map[uint64]func(json.RawMessage, *wireError){}, subs: map[string]struct{}{}, done: make(chan struct{}),
	}
	c.relays = map[string]*relayLink{"nas": l}
	return c, l, hostSide
}

// TestClosingAConnectionWritesNothingThroughItsLinks_REQ_API_006: a connection is closed on the
// fan-out's goroutine when its client stops reading. Its links end with it, and a call still
// waiting on a host must not be answered then: the answer would wait on the very writer the
// stalled client holds, and every other client's notifications with it.
func TestClosingAConnectionWritesNothingThroughItsLinks_REQ_API_006(t *testing.T) {
	t.Parallel()
	c, l, _ := relayFixture(t)
	l.pending[1] = func(json.RawMessage, *wireError) { c.writeRaw(map[string]string{"answer": "late"}) }
	l.subs["ses_1"] = struct{}{}

	// A writer stuck on the stalled client holds the connection's write lock.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	closed := make(chan struct{})
	go func() {
		_ = c.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the connection waits on its writer: one stalled client stops the fan-out")
	}
}

// TestAFailedRelayWriteIsAnsweredOnce_REQ_RMT_002: when the request cannot be written to the
// host, its caller answers it with the error, and the link's end does not answer it again.
func TestAFailedRelayWriteIsAnsweredOnce_REQ_RMT_002(t *testing.T) {
	t.Parallel()
	_, l, hostSide := relayFixture(t)
	_ = hostSide.Close()
	var answered atomic.Int32
	if _, err := l.call("pane.split", json.RawMessage(`{}`), func(json.RawMessage, *wireError) { answered.Add(1) }); err == nil {
		t.Fatal("a write to a host that is gone succeeded")
	}
	if n := answered.Load(); n != 0 {
		t.Fatalf("the failed request was also answered by the link's end, %d times", n)
	}
}
