package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

const fakeSessionID = "ses_01J9Z3K8T2QH6W4V5X7Y8Z9A0B"

// fakeSessions is a ports.Sessions that answers from memory.
//
// The fan-out is what these tests are about, and a real PTY would make them slow and
// flaky without exercising a single line of the batching, the queue budget or the
// snapshot-then-live ordering. The real adapters have their own tests.
type fakeSessions struct {
	mu       sync.Mutex
	snapshot sessports.Snapshot
	// onSnapshot runs while the snapshot is being taken. It exists so a test can put
	// output into the exact window between subscribing and snapshotting, which is where
	// a chunk would be lost if the order were wrong.
	onSnapshot func()
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{snapshot: sessports.Snapshot{Data: []byte("empty screen")}}
}

func (f *fakeSessions) setSnapshot(data []byte, seq uint64, cursorX, cursorY uint16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot = sessports.Snapshot{Data: data, Seq: seq, CursorX: cursorX, CursorY: cursorY}
}

func (f *fakeSessions) Snapshot(_ context.Context, id string) (sessports.Snapshot, error) {
	if id != fakeSessionID {
		return sessports.Snapshot{}, fmt.Errorf("%w: %s", sessdomain.ErrNotFound, id)
	}

	f.mu.Lock()
	hook := f.onSnapshot
	f.mu.Unlock()
	if hook != nil {
		hook()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}

// duringSnapshot makes fn run inside the next Snapshot call.
func (f *fakeSessions) duringSnapshot(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSnapshot = fn
}

func (f *fakeSessions) Get(_ context.Context, id string) (sessdomain.Session, error) {
	if id != fakeSessionID {
		return sessdomain.Session{}, fmt.Errorf("%w: %s", sessdomain.ErrNotFound, id)
	}
	return sessdomain.Session{
		ID: id, Shell: "/bin/bash", CWD: "/tmp",
		Size:  sessdomain.Size{Cols: 80, Rows: 24},
		State: sessdomain.StateAlive, Integration: sessdomain.IntegrationPending,
		InputOwner: sessdomain.InputOwnerHuman, CreatedAt: time.Unix(0, 0),
	}, nil
}

func (f *fakeSessions) Create(ctx context.Context, _ sessdomain.CreateParams) (sessdomain.Session, error) {
	return f.Get(ctx, fakeSessionID)
}

func (f *fakeSessions) List(ctx context.Context) ([]sessdomain.Session, error) {
	session, err := f.Get(ctx, fakeSessionID)
	if err != nil {
		return nil, err
	}
	return []sessdomain.Session{session}, nil
}

func (f *fakeSessions) Input(_ context.Context, id string, _ []byte, _ sessdomain.InputOwner) error {
	return f.exists(id)
}

func (f *fakeSessions) Resize(_ context.Context, id string, _ sessdomain.Size) error {
	return f.exists(id)
}

func (f *fakeSessions) SetInputOwner(_ context.Context, id string, _ sessdomain.InputOwner) error {
	return f.exists(id)
}

func (f *fakeSessions) Close(_ context.Context, id string) error { return f.exists(id) }

func (f *fakeSessions) exists(id string) error {
	if id != fakeSessionID {
		return fmt.Errorf("%w: %s", sessdomain.ErrNotFound, id)
	}
	return nil
}

// Compile-time proof that the fake really satisfies the port it stands in for. Without it
// a change to the interface would leave these tests passing against a shape the daemon no
// longer uses.
var _ sessports.Sessions = (*fakeSessions)(nil)

// testServerWithSessions starts a server wired to a fake sessions module, with the
// notification dispatcher running.
func testServerWithSessions(t *testing.T, sessions sessports.Sessions) *Server {
	t.Helper()

	dir := t.TempDir()
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)

	s, err := Listen(t.Context(), Config{
		SocketPath:    filepath.Join(dir, SocketFileName),
		TokenPath:     filepath.Join(dir, TokenFileName),
		DaemonVersion: "0.1.0-test",
		Sessions:      sessions,
		Bus:           eventBus,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	go s.Notify(ctx)

	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})
	return s
}

// dispatchTestOutput publishes one output chunk through the bus, the same path the sessions
// module uses.
func (s *Server) dispatchTestOutput(sessionID string, seq uint64, data []byte) {
	s.cfg.Bus.Publish(sessports.SessionOutput{SessionID: sessionID, Seq: seq, Data: data})
}

// benchServer is testServerWithSessions for a benchmark, which has no *testing.T.
func benchServer(b *testing.B, sessions sessports.Sessions) *Server {
	b.Helper()

	dir := b.TempDir()
	eventBus := bus.New()
	b.Cleanup(eventBus.Close)

	s, err := Listen(b.Context(), Config{
		SocketPath:    filepath.Join(dir, SocketFileName),
		TokenPath:     filepath.Join(dir, TokenFileName),
		DaemonVersion: "0.1.0-bench",
		Sessions:      sessions,
		Bus:           eventBus,
	})
	if err != nil {
		b.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(b.Context())
	go func() { _ = s.Serve(ctx) }()
	go s.Notify(ctx)
	b.Cleanup(func() {
		cancel()
		_ = s.Close()
	})
	return s
}

// benchClient dials, completes the handshake and subscribes, so a benchmark measures the
// streaming path and not the setup.
func benchClient(b *testing.B, s *Server) *client {
	b.Helper()

	var dialer net.Dialer
	netConn, err := dialer.DialContext(b.Context(), "unix", s.SocketPath())
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	b.Cleanup(func() { _ = netConn.Close() })

	c := &client{conn: netConn, dec: json.NewDecoder(netConn)}
	send := func(id int, method string, params any) {
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": params,
		})
		if err != nil {
			b.Fatalf("marshal: %v", err)
		}
		if _, err := netConn.Write(append(body, '\n')); err != nil {
			b.Fatalf("write: %v", err)
		}
		var resp response
		if err := c.dec.Decode(&resp); err != nil {
			b.Fatalf("read %s reply: %v", method, err)
		}
		if resp.Error != nil {
			b.Fatalf("%s: %+v", method, resp.Error)
		}
	}

	send(1, "system.hello", map[string]any{
		"token": s.Token(), "client_kind": "tui", "protocol_version": ProtocolVersion,
	})
	send(2, "session.subscribe", map[string]any{"session_id": fakeSessionID})
	return c
}

// hasSubscription reports whether any connection is currently subscribed to the session.
// It exists so a test can assert *when* the subscription was registered, rather than
// hoping the dispatch goroutine happens to run inside the window being tested.
func (s *Server) hasSubscription(sessionID string) bool {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.subsMu.Lock()
		_, ok := c.subs[sessionID]
		c.subsMu.Unlock()
		if ok {
			return true
		}
	}
	return false
}
