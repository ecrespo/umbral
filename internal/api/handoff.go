package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

)

// A daemon handoff (REQ-HND-001, delta `2026-10-pty-handoff`) suspends the server: it stops
// accepting and closes every client, which reconnects. The listening socket then goes to the
// next daemon, and the clients that connect meanwhile wait in its backlog for whichever daemon
// accepts next — the new one, or this one again after a failure (REQ-HND-002).

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
