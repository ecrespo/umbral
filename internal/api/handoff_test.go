//go:build unix

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// TestASuspendedServerHandsItsSocketOver_REQ_HND_001: a suspended server closes its clients and
// stops accepting, while a client that connects meanwhile is held by the kernel. A second
// server built on the first's listener serves that client. The first, abandoned, leaves the
// socket's path for it (delta `2026-10-pty-handoff`).
func TestASuspendedServerHandsItsSocketOver_REQ_HND_001(t *testing.T) {
	old := testServer(t, nil)
	before := dial(t, old)
	if resp := before.hello(old.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("hello: %+v", resp.Error)
	}

	if err := old.Suspend(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The client of the old server is closed: it reconnects.
	if err := before.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var netErr net.Error
	if _, err := before.conn.Read(make([]byte, 1)); err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("a client of the suspended server is still connected: %v", err)
	}

	// A client connecting during the handoff is accepted by nobody yet.
	waiting := dial(t, old)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "system.hello", "params": map[string]any{
		"token": old.Token(), "client_kind": string(ClientCLI), "client_version": "t", "protocol_version": ProtocolVersion,
	}})
	waiting.send(append(body, '\n'))
	if err := waiting.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, _ := waiting.conn.Read(make([]byte, 1)); n > 0 {
		t.Fatal("the suspended server answered a new client")
	}

	listener, err := old.ListenerFile()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.FileListener(listener)
	_ = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	old.Abandon()

	next, err := Listen(t.Context(), Config{
		SocketPath: old.SocketPath(), TokenPath: old.cfg.TokenPath, DaemonVersion: "next", Listener: ln.(*net.UnixListener),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- next.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = next.Close()
		<-served
	})

	if next.Token() != old.Token() {
		t.Fatal("the next server made a new token")
	}
	if resp := waiting.read(); resp.Error != nil {
		t.Fatalf("the client held during the handoff was refused: %+v", resp.Error)
	}
	// The old daemon's way out closes its server: the path stays for the next one.
	_ = old.Close()
	if _, err := os.Stat(old.SocketPath()); err != nil {
		t.Fatalf("the abandoned server removed the socket: %v", err)
	}
	if resp := dial(t, next).hello(next.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("a new client of the next server: %+v", resp.Error)
	}
}

// TestAResumedServerServesAgain_REQ_HND_002: after a failed handoff the server accepts again,
// the client that connected meanwhile included.
func TestAResumedServerServesAgain_REQ_HND_002(t *testing.T) {
	s := testServer(t, nil)
	if err := s.Suspend(t.Context()); err != nil {
		t.Fatal(err)
	}
	waiting := dial(t, s)
	s.Resume()
	if resp := waiting.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("after Resume: %+v", resp.Error)
	}
	if resp := dial(t, s).hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("a new client after Resume: %+v", resp.Error)
	}
}
