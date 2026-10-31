package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/config"
)

// A daemon handoff (REQ-HND-001, delta `2026-10-pty-handoff`) suspends the server: it stops
// accepting and closes every client, which reconnects. The listening socket then goes to the
// next daemon, and the clients that connect meanwhile wait in its backlog for whichever daemon
// accepts next — the new one, or this one again after a failure (REQ-HND-002).

// bind is the listening socket: the inherited one, or a fresh one at SocketPath.
func bind(ctx context.Context, cfg Config) (net.Listener, error) {
	if cfg.Listener != nil {
		return cfg.Listener, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), config.RuntimeDirMode); err != nil {
		return nil, fmt.Errorf("api: create the runtime directory: %w", err)
	}
	// A socket left behind by a crashed daemon would make Listen fail with EADDRINUSE,
	// so it is removed. What makes that safe is the instance lock `cmd/umbrald` takes on
	// the runtime directory before it gets here: a live daemon holds it, so anything
	// still on disk belongs to a daemon that is gone. Nothing else may call New without
	// holding that lock — an earlier comment here claimed a database lock did the job,
	// and there is no such lock.
	if err := removeStaleSocket(cfg.SocketPath); err != nil {
		return nil, err
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("api: listen on %s: %w", cfg.SocketPath, err)
	}
	// net.Listen applies the umask, so the mode is set explicitly rather than assumed.
	// This is REQ-SEC-007 and it is checked by a test, not by inspection.
	if err := os.Chmod(cfg.SocketPath, socketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api: set socket permissions: %w", err)
	}
	return ln, nil
}

// suspension is a handoff's hold on the accept loop: parked is closed when Serve has stopped
// accepting, resume when it may go on, or end if the server was abandoned.
type suspension struct {
	parked     chan struct{}
	parkedOnce sync.Once
	resume     chan struct{}
	abandoned  bool
}

// Suspend stops accepting, closes every client and waits for their handlers to end, so no
// request is half-done when the daemon's state is handed over.
func (s *Server) Suspend(ctx context.Context) error {
	ul, ok := s.listener.(*net.UnixListener)
	if !ok {
		return errors.New("api: the listener is not a Unix socket")
	}
	s.mu.Lock()
	if s.suspended != nil {
		s.mu.Unlock()
		return errors.New("api: already suspended")
	}
	sp := &suspension{parked: make(chan struct{}), resume: make(chan struct{})}
	s.suspended = sp
	s.mu.Unlock()

	if err := ul.SetDeadline(time.Unix(1, 0)); err != nil {
		s.Resume()
		return fmt.Errorf("api: stop accepting: %w", err)
	}
	select {
	case <-sp.parked:
	case <-ctx.Done():
		s.Resume()
		return ctx.Err()
	}
	s.closeConns()
	s.wg.Wait()
	return nil
}

// Resume accepts again after a handoff that failed.
func (s *Server) Resume() {
	s.mu.Lock()
	sp := s.suspended
	s.suspended = nil
	s.mu.Unlock()
	if sp == nil {
		return
	}
	if ul, ok := s.listener.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Time{})
	}
	close(sp.resume)
}

// ListenerFile is another descriptor for the listening socket, for the next daemon.
func (s *Server) ListenerFile() (*os.File, error) {
	ul, ok := s.listener.(*net.UnixListener)
	if !ok {
		return nil, errors.New("api: the listener is not a Unix socket")
	}
	f, err := ul.File()
	if err != nil {
		return nil, fmt.Errorf("api: the listener's descriptor: %w", err)
	}
	return f, nil
}

// Abandon ends a suspended server for good once the next daemon has its socket: Serve
// returns, and Close leaves the socket's path, which the next daemon serves.
func (s *Server) Abandon() {
	s.mu.Lock()
	sp := s.suspended
	s.abandoned = true
	s.mu.Unlock()
	if ul, ok := s.listener.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if sp != nil {
		sp.abandoned = true
		close(sp.resume)
	}
}

// parkIfSuspended is Serve's answer to an accept error: true when it came from Suspend and
// the loop should go on accepting after Resume; abandoned when it should stop.
func (s *Server) parkIfSuspended() (resumed, abandoned bool) {
	s.mu.Lock()
	sp := s.suspended
	s.mu.Unlock()
	if sp == nil {
		return false, false
	}
	sp.parkedOnce.Do(func() { close(sp.parked) })
	<-sp.resume
	return !sp.abandoned, sp.abandoned
}
