//go:build unix

package handoff_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/handoff"
)

// socketDir is a short directory for a socket: a macOS TMPDIR is too long for sun_path.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hnd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func pair(t *testing.T) (server, client *handoff.Conn) {
	t.Helper()
	path := filepath.Join(socketDir(t), handoff.SocketName)
	ln, err := handoff.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan *handoff.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
		}
		accepted <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err = handoff.Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	if server == nil {
		t.FailNow()
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return server, client
}

type frame struct {
	Kind string
	Body []byte
}

// TestFramesCarryTheirDescriptors_REQ_HND_001: a frame and the descriptors beside it arrive
// together, in order, and a descriptor received is the same open file the sender had — a byte
// written through one end of a pipe passed over the socket is read from the other.
func TestFramesCarryTheirDescriptors_REQ_HND_001(t *testing.T) {
	server, client := pair(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	// A frame larger than one socket buffer, so the receiver has to reassemble it.
	big := []byte(strings.Repeat("x", 1<<20))
	sent := make(chan error, 1)
	go func() {
		if err := client.Send(frame{Kind: "first"}); err != nil {
			sent <- err
			return
		}
		sent <- client.Send(frame{Kind: "session", Body: big}, w)
	}()

	var got frame
	files, err := server.Recv(&got)
	if err != nil || got.Kind != "first" || len(files) != 0 {
		t.Fatalf("first frame: %+v, %d files, %v", got.Kind, len(files), err)
	}
	files, err = server.Recv(&got)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if got.Kind != "session" || len(got.Body) != len(big) {
		t.Fatalf("second frame: %q with %d bytes, want session with %d", got.Kind, len(got.Body), len(big))
	}
	if len(files) != 1 {
		t.Fatalf("%d descriptors arrived, want 1", len(files))
	}
	defer func() { _ = files[0].Close() }()

	if _, err := files[0].Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(r, b); err != nil || b[0] != 'z' {
		t.Fatalf("the descriptor received is not the pipe sent: %q, %v", b, err)
	}
}

// TestASocketLeftByAGoneDaemonIsReplaced: Listen takes over a path a dead daemon left, and
// sets the socket to 0600 whatever the umask.
func TestASocketLeftByAGoneDaemonIsReplaced(t *testing.T) {
	path := filepath.Join(socketDir(t), handoff.SocketName)
	first, err := handoff.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	first.Abandon()

	second, err := handoff.Listen(path)
	if err != nil {
		t.Fatalf("a stale socket stopped the listener: %v", err)
	}
	defer func() { _ = second.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the handoff socket is %o, want 600", mode)
	}
}

// TestAClosedPeerIsEOF: a peer that goes away is io.EOF, which the old daemon reads as a
// failed handoff and resumes on (REQ-HND-002).
func TestAClosedPeerIsEOF_REQ_HND_002(t *testing.T) {
	server, client := pair(t)
	_ = client.Close()
	var got frame
	if _, err := server.Recv(&got); !errors.Is(err, io.EOF) {
		t.Fatalf("a closed peer reads as %v, want io.EOF", err)
	}
}

// TestAStalledPeerTimesOut_REQ_HND_002: Recv honours the connection's deadline, which is the
// old daemon's 30 s bound on a new daemon that stops answering.
func TestAStalledPeerTimesOut_REQ_HND_002(t *testing.T) {
	server, _ := pair(t)
	if err := server.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var got frame
	if _, err := server.Recv(&got); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a stalled peer reads as %v, want a deadline", err)
	}
}

// TestAnOversizedFrameIsRefused: a length past MaxFrame is refused before anything is
// allocated for it.
func TestAnOversizedFrameIsRefused(t *testing.T) {
	server, client := pair(t)
	go func() { _ = client.SendRaw([]byte{0xff, 0xff, 0xff, 0xff}) }()
	var got frame
	if _, err := server.Recv(&got); err == nil || !strings.Contains(err.Error(), "frame") {
		t.Fatalf("an oversized frame read as %v", err)
	}
}

// TestSendingADescriptorKeepsItsDeadlines_REQ_HND_002: a descriptor sent stays non-blocking on
// this side, so a deadline still wakes an Accept on it. os.File.Fd makes a file blocking for
// every descriptor of its open file description, and a sender that used it left the old
// daemon unable to pause its socket for the next handoff after a failed one.
func TestSendingADescriptorKeepsItsDeadlines_REQ_HND_002(t *testing.T) {
	server, client := pair(t)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(socketDir(t), "l.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	f, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	go func() { _ = client.Send(frame{Kind: "listener"}, f) }()
	var got frame
	files, err := server.Recv(&got)
	if err != nil {
		t.Fatal(err)
	}
	closeFiles(files)

	if err := ln.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		accepted <- err
	}()
	select {
	case err := <-accepted:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("the accept ended with %v, want its deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a deadline no longer wakes an accept on the socket that was sent")
	}
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// TestAnAcceptedPeerHasItsPid_REQ_HND_001: the accepted side knows the pid of the process that
// connected, from the kernel, which the old daemon checks against the agents' terminals.
func TestAnAcceptedPeerHasItsPid_REQ_HND_001(t *testing.T) {
	server, _ := pair(t)
	if got := server.PeerPID(); got != os.Getpid() {
		t.Fatalf("the peer's pid is %d, want %d", got, os.Getpid())
	}
}
