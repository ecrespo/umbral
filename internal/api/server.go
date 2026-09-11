package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// ClientKind is the caller's role (API Spec §2). It decides which methods are reachable.
type ClientKind string

// The client kinds the daemon serves. `cli` is restricted to the method subset in
// API Spec §2; `desktop` arrives with the Wails client in F2.
const (
	ClientTUI     ClientKind = "tui"
	ClientCLI     ClientKind = "cli"
	ClientDesktop ClientKind = "desktop"
)

// valid reports whether the handshake named a kind the daemon knows.
func (k ClientKind) valid() bool {
	switch k {
	case ClientTUI, ClientCLI, ClientDesktop:
		return true
	default:
		return false
	}
}

// capabilities reports the method namespaces this daemon actually serves, which is what
// the handshake advertises (API Spec §2).
//
// It is derived from the method table rather than written by hand, because a hand-written
// list drifts: advertising "sessions" before session.create exists tells a client to take
// a branch that cannot work. "system" is excluded because every client can always call it.
func (s *Server) capabilities() []string {
	seen := make(map[string]struct{}, len(s.methods))
	for name := range s.methods {
		namespace, _, found := strings.Cut(name, ".")
		if !found || namespace == "system" {
			continue
		}
		seen[namespace] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for namespace := range seen {
		out = append(out, namespace)
	}
	sort.Strings(out)
	return out
}

// StatusFunc reports the daemon's state for system.status. The composition root supplies
// it, which is how api avoids importing the modules it reports on.
type StatusFunc func(ctx context.Context) (StatusResult, error)

// Config configures a Server. SocketPath, TokenPath and DaemonVersion are required.
type Config struct {
	SocketPath    string
	TokenPath     string
	DaemonVersion string
	// Status is called by system.status. A nil Status reports an empty daemon, which
	// is what F0 has before sessions exist.
	Status StatusFunc
	// Sessions is the terminal module's inbound port. A nil value leaves the session.*
	// methods answering METHOD_NOT_FOUND, which is what the daemon does before T-F0-05
	// is wired in.
	Sessions sessports.Sessions
	Bus      *bus.Bus
	Logger   *slog.Logger
}

// Server accepts client connections on the Unix socket and dispatches JSON-RPC methods.
type Server struct {
	cfg       Config
	token     string
	listener  net.Listener
	startedAt time.Time
	methods   map[string]method

	wg sync.WaitGroup

	mu    sync.Mutex
	conns map[*conn]struct{}
}

// Listen creates the runtime directory, loads or creates the token and binds the socket
// with mode 0600 (REQ-SEC-007). It does not serve yet; call Serve.
func Listen(ctx context.Context, cfg Config) (*Server, error) {
	if cfg.SocketPath == "" || cfg.TokenPath == "" {
		return nil, errors.New("api: SocketPath and TokenPath are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Bus == nil {
		cfg.Bus = bus.New()
	}

	token, err := LoadOrCreateToken(cfg.TokenPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), runtimeDirMode); err != nil {
		return nil, fmt.Errorf("api: create the runtime directory: %w", err)
	}
	// A socket left behind by a crashed daemon would make Listen fail with EADDRINUSE.
	// Removing it is safe here because a live daemon holds a lock on the database.
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

	s := &Server{
		cfg:       cfg,
		token:     token,
		listener:  ln,
		startedAt: time.Now(),
		conns:     make(map[*conn]struct{}),
	}
	s.methods = s.registry()
	return s, nil
}

// SocketPath reports the bound socket, which is useful when the caller let the server
// choose it.
func (s *Server) SocketPath() string { return s.cfg.SocketPath }

// Token reports the per-installation token. Clients read it from the token file; this
// accessor exists for the composition root and for tests.
func (s *Server) Token() string { return s.token }

// Serve accepts connections until ctx is cancelled or Close is called.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		// Closing the listener stops new connections; closing the live ones unblocks
		// their read loops. Without the second step a client that simply stops talking
		// keeps the daemon from shutting down.
		_ = s.listener.Close()
		s.closeConns()
	}()

	for {
		netConn, acceptErr := s.listener.Accept()
		if acceptErr != nil {
			// A closed listener is how Close and context cancellation stop this loop;
			// that is a clean stop, not a failure to report.
			if isShutdown(ctx, acceptErr) {
				s.wg.Wait()
				return nil
			}
			return fmt.Errorf("api: accept: %w", acceptErr)
		}

		c := s.newConn(netConn)
		s.track(c)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(c)
			c.serve(ctx)
		}()
	}
}

// Close stops accepting, closes every live connection and removes the socket file.
func (s *Server) Close() error {
	err := s.listener.Close()

	s.closeConns()
	s.wg.Wait()
	if rmErr := os.Remove(s.cfg.SocketPath); rmErr != nil && !os.IsNotExist(rmErr) {
		return errors.Join(err, fmt.Errorf("api: remove socket: %w", rmErr))
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) track(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[c] = struct{}{}
}

// isShutdown reports whether an Accept error means the server is stopping rather than
// failing.
func isShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, net.ErrClosed)
}

// closeConns closes every live connection, which unblocks the read loop each one is
// parked in. Safe to call more than once: conn.close goes through a sync.Once.
func (s *Server) closeConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.close()
	}
}

func (s *Server) untrack(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

// removeStaleSocket deletes a leftover socket file, refusing to touch anything that is
// not a socket so a mistyped path cannot delete a regular file.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("api: inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("api: %s exists and is not a socket; refusing to remove it", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("api: remove the stale socket %s: %w", path, err)
	}
	return nil
}
