// Package remote keeps the links to the remote daemons the user added (REQ-RMT-001…003): one
// supervisor per host asks the host's daemon for its socket and token over ssh, forwards that
// socket to a local one, and reconnects with backoff when the forward ends. What crosses a link is
// internal/api's business; this module only keeps it open.
package remote

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/remote/domain"
	"github.com/ecrespo/umbral/internal/remote/ports"
)

// Options builds a Manager.
type Options struct {
	Store     ports.Store
	Tunnels   ports.Tunnels
	Publisher ports.Publisher
	// Dir holds the forwarded sockets, one per host; it is created 0700.
	Dir    string
	Logger *slog.Logger
	// Backoff is the wait before reconnect attempt n; domain.Backoff when nil.
	Backoff func(attempt int) time.Duration
	// Now is the clock for created_at; time.Now when nil.
	Now func() time.Time
	// Probe checks that the host's daemon answers its handshake through a forward that started:
	// ssh opens the remote socket per connection, so a forward says nothing of the daemon. A
	// host is `connected` only once the probe passes; nil skips it.
	Probe func(ctx context.Context, link ports.Link) error
}

// probeTimeout bounds a probe of a host's daemon.
const probeTimeout = 15 * time.Second

// maxSocketPath is the longest Unix socket path every supported OS binds: macOS's sun_path holds
// 104 bytes with the terminating NUL.
const maxSocketPath = 103

// Manager is ports.Hosts.
type Manager struct {
	opts Options

	mu    sync.Mutex
	ctx   context.Context
	hosts map[string]*supervisor
	wg    sync.WaitGroup
}

var _ ports.Hosts = (*Manager)(nil)

// supervisor is one host's link and the goroutine that keeps it.
type supervisor struct {
	host   domain.Host
	target domain.Target
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	state     domain.State
	lastError string
	link      ports.Link
}

// New builds a Manager; Start brings up the stored hosts.
func New(o Options) (*Manager, error) {
	if o.Store == nil || o.Tunnels == nil || o.Publisher == nil || o.Dir == "" {
		return nil, errors.New("remote: a store, tunnels, a publisher and a directory are required")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Backoff == nil {
		o.Backoff = domain.Backoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{opts: o, hosts: map[string]*supervisor{}}, nil
}

// Start creates the sockets' directory and links every stored host. The links end with ctx.
func (m *Manager) Start(ctx context.Context) error {
	if err := os.MkdirAll(m.opts.Dir, 0o700); err != nil {
		return fmt.Errorf("remote: create %s: %w", m.opts.Dir, err)
	}
	// MkdirAll keeps the mode of a directory that exists; a forwarded socket reaches a daemon,
	// so nobody else may list or reach it.
	if err := os.Chmod(m.opts.Dir, 0o700); err != nil { //nolint:gosec // a directory: 0700 is its owner's alone
		return fmt.Errorf("remote: chmod %s: %w", m.opts.Dir, err)
	}
	sweep(m.opts.Dir, m.opts.Logger)
	hosts, err := m.opts.Store.List(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx = ctx
	for _, h := range hosts {
		target, err := domain.ParseURL(h.URL)
		if err != nil {
			m.opts.Logger.Warn("a stored remote host has an invalid url; it is not linked", slog.String("name", h.Name))
			continue
		}
		m.startLocked(h, target) //nolint:contextcheck // a link lives as long as the manager's context, not a request's
	}
	return nil
}

// Close stops every link and waits for them.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, s := range m.hosts {
		s.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// List implements ports.Hosts.
func (m *Manager) List(context.Context) ([]ports.Status, error) {
	m.mu.Lock()
	out := make([]ports.Status, 0, len(m.hosts))
	for _, s := range m.hosts {
		out = append(out, s.status())
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b ports.Status) int { return cmp.Compare(a.Host.Name, b.Host.Name) })
	return out, nil
}

// Add implements ports.Hosts.
func (m *Manager) Add(ctx context.Context, name, url string) (ports.Status, error) {
	if err := domain.ValidateName(name); err != nil {
		return ports.Status{}, err
	}
	target, err := domain.ParseURL(url)
	if err != nil {
		return ports.Status{}, err
	}
	if local := filepath.Join(m.opts.Dir, socketName(name, os.Getpid())); len(local) > maxSocketPath {
		return ports.Status{}, fmt.Errorf("%w: the host's forwarded socket %s would be longer than %d bytes; use a shorter name", domain.ErrValidation, local, maxSocketPath)
	}
	h := domain.Host{Name: name, URL: url, CreatedAt: m.opts.Now().UnixMilli()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return ports.Status{}, errors.New("remote: the manager is not started")
	}
	if _, ok := m.hosts[name]; ok {
		return ports.Status{}, fmt.Errorf("%w: %s", domain.ErrConflict, name)
	}
	if err := m.opts.Store.Insert(ctx, h); err != nil {
		return ports.Status{}, err
	}
	s := m.startLocked(h, target) //nolint:contextcheck // a link lives as long as the manager's context, not the request's
	return s.status(), nil
}

// Remove implements ports.Hosts.
func (m *Manager) Remove(ctx context.Context, name string) error {
	if err := m.opts.Store.Delete(ctx, name); err != nil {
		return err
	}
	m.mu.Lock()
	s, ok := m.hosts[name]
	delete(m.hosts, name)
	m.mu.Unlock()
	if ok {
		s.cancel()
		<-s.done
	}
	return nil
}

// Known implements ports.Hosts.
func (m *Manager) Known(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.hosts[name]
	return ok
}

// Link implements ports.Hosts.
func (m *Manager) Link(name string) (ports.Link, bool) {
	m.mu.Lock()
	s, ok := m.hosts[name]
	m.mu.Unlock()
	if !ok {
		return ports.Link{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != domain.StateConnected {
		return ports.Link{}, false
	}
	return s.link, true
}

func (m *Manager) startLocked(h domain.Host, target domain.Target) *supervisor {
	ctx, cancel := context.WithCancel(m.ctx)
	s := &supervisor{host: h, target: target, cancel: cancel, done: make(chan struct{}), state: domain.StateConnecting}
	m.hosts[h.Name] = s
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(s.done)
		m.keep(ctx, s)
	}()
	return s
}

// keep links the host until ctx ends: it asks for the endpoint, forwards it, waits for the
// forward to end and tries again after a backoff that grows with each failure in a row and starts
// over once a link held.
func (m *Manager) keep(ctx context.Context, s *supervisor) {
	local := filepath.Join(m.opts.Dir, socketName(s.host.Name, os.Getpid()))
	defer func() { _ = os.Remove(local) }()
	attempt := 0
	for {
		held, err := m.linkOnce(ctx, s, local)
		if ctx.Err() != nil {
			return
		}
		if held {
			attempt = 0
		}
		m.set(s, domain.StateDisconnected, describe(err), ports.Link{})
		m.opts.Logger.Info("remote host disconnected", slog.String("name", s.host.Name), slog.String("reason", describe(err)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.opts.Backoff(attempt)):
		}
		attempt++
		m.set(s, domain.StateConnecting, "", ports.Link{})
	}
}

// linkOnce brings the link up and holds it until it ends; held says it came up.
func (m *Manager) linkOnce(ctx context.Context, s *supervisor, local string) (held bool, err error) {
	askCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ep, err := m.opts.Tunnels.Endpoint(askCtx, s.target)
	cancel()
	if err != nil {
		return false, err
	}
	if err := domain.ValidateEndpoint(ep); err != nil {
		return false, err
	}
	_ = os.Remove(local)
	fw, err := m.opts.Tunnels.Forward(ctx, s.target, local, ep.Socket)
	if err != nil {
		return false, err
	}
	defer func() { _ = fw.Close() }()
	link := ports.Link{Socket: local, Token: ep.Token}
	if m.opts.Probe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		err := m.opts.Probe(probeCtx, link)
		cancel()
		if err != nil {
			return false, fmt.Errorf("the host's daemon does not answer: %w", err)
		}
	}
	m.set(s, domain.StateConnected, "", link)
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-fw.Done():
		return true, fw.Err()
	}
}

// set records and publishes a state change; a change to the same state and reason is not
// published again.
func (m *Manager) set(s *supervisor, state domain.State, lastError string, link ports.Link) {
	s.mu.Lock()
	same := s.state == state && s.lastError == lastError
	s.state, s.lastError, s.link = state, lastError, link
	s.mu.Unlock()
	if same && state != domain.StateDisconnected {
		return
	}
	m.opts.Publisher.Publish(ports.HostState{Name: s.host.Name, State: state, LastError: lastError})
}

func (s *supervisor) status() ports.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ports.Status{Host: s.host, State: s.state, LastError: s.lastError}
}

// describe makes an error one short line for `last_error`.
func describe(err error) string {
	if err == nil {
		return "the link ended"
	}
	line := strings.TrimSpace(err.Error())
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[i+1:])
	}
	const maxLen = 200
	if len(line) > maxLen {
		line = line[:maxLen]
	}
	return line
}

// socketName is a host's forwarded socket in the directory. It carries the daemon's pid: across a
// live handoff (REQ-HND-001) the outgoing daemon's forwards and the incoming one's live side by
// side, and neither may remove the other's.
func socketName(host string, pid int) string {
	return fmt.Sprintf("%s.%d.sock", host, pid)
}

// sweep removes the sockets a daemon that is gone left behind, as a kill -9 does.
func sweep(dir string, logger *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		parts := strings.Split(e.Name(), ".")
		if len(parts) != 3 || parts[2] != "sock" {
			continue
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil || pid <= 0 || alive(pid) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			logger.Info("removed a forwarded socket left by a daemon that is gone", slog.String("file", e.Name()))
		}
	}
}
