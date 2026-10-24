package api

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestASlowClientHoldsUpNoOtherClient_REQ_API_006: one client completes the handshake and
// stops reading. The daemon keeps broadcasting to the other at full speed, closes the stalled
// one once it is more than the queue limit behind, and the dispatcher never waits for it.
//
// Before T-HRD-04 the broadcast wrote to each socket in turn on the dispatch goroutine, so the
// first notification that did not fit the stalled client's socket buffer stopped every client.
func TestASlowClientHoldsUpNoOtherClient_REQ_API_006(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	stalled := dial(t, s)
	if resp := stalled.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	reader := dial(t, s)
	if resp := reader.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	// Enough to overflow the stalled client's queue with its socket buffer full besides.
	const size = 64 << 10
	count := ClientNotificationQueueBytes/size + 64
	body := strings.Repeat("x", size)

	var got atomic.Int64
	received := make(chan int, 1)
	go func() {
		// A plain line reader: what matters is that every broadcast reaches this client.
		r := bufio.NewReaderSize(reader.conn, 1<<20)
		_ = reader.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n := 0
		for n < count {
			line, err := r.ReadSlice('\n')
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				break
			}
			if len(line) > size {
				n++
				got.Store(int64(n))
			}
		}
		received <- n
	}()

	// The broadcasts are paced to the reading client, at most 2 MiB ahead of it, so only the
	// stalled one passes the limit. An unpaced burst over 8 MiB closes a reading client too
	// when its writer is not scheduled in time, as with one CPU; that is the limit doing its job.
	const ahead = 32
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := range count {
			for int64(i)-got.Load() > ahead {
				time.Sleep(time.Millisecond)
			}
			s.broadcast("test.large", map[string]string{"body": body}, s.nextSeq())
		}
	}()
	select {
	case <-sent:
	case <-time.After(20 * time.Second):
		t.Fatal("the dispatcher is still writing after 20 s: a client that stopped reading holds it up")
	}

	select {
	case n := <-received:
		if n != count {
			t.Fatalf("the reading client got %d of %d notifications", n, count)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the reading client did not receive every notification")
	}

	// The stalled client was closed: once it drains what reached its socket, it reads an end.
	if err := stalled.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := io.Copy(io.Discard, stalled.conn)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("the stalled client's connection is still open past the queue limit")
	}
}

// TestASlowClientIsClosedNotTrimmed_REQ_API_006: past the limit nothing is queued for it any
// more, and the queue's own writer is told to stop. A dropped control notification would leave
// the client's tree silently different from the daemon's; a closed connection says so.
func TestASlowClientIsClosedNotTrimmed_REQ_API_006(t *testing.T) {
	t.Parallel()

	// A line over the limit on its own is written, not taken for a stalled client: the frame
	// limit, which may be higher, decides its size.
	if q := newOutbox(); !q.push(make([]byte, ClientNotificationQueueBytes+1)) {
		t.Fatal("an empty queue refused a line larger than its limit")
	}

	q := newOutbox()
	line := make([]byte, 64<<10)
	for q.queued+len(line) <= ClientNotificationQueueBytes {
		if !q.push(line) {
			t.Fatalf("the queue refused a line at %d bytes, under the %d limit", q.queued, ClientNotificationQueueBytes)
		}
	}
	if q.push(line) {
		t.Fatalf("the queue accepted %d bytes, past the %d limit", q.queued+len(line), ClientNotificationQueueBytes)
	}
	if q.queued != 0 || len(q.lines) != 0 {
		t.Errorf("an overflowed queue still holds %d bytes in %d lines", q.queued, len(q.lines))
	}
	if q.push([]byte("small\n")) {
		t.Error("an overflowed queue accepted another line")
	}
	if _, ok := q.pop(); ok {
		t.Error("an overflowed queue handed its writer a line")
	}
}
