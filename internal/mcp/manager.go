// Package mcp is the MCP client (REQ-MCP-001…004): it connects the servers the user
// configured, hands their tools to the tool registry, reconnects a server that is lost, and
// serves mcp.server.*. It reaches storage, the SDK and the registry only through its ports,
// which cmd/umbrald wires.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/mcp/ports"
)

// Config wires the manager. Every field but the last four is required.
type Config struct {
	Store     ports.Store
	Connector ports.Connector
	Secrets   ports.Secrets
	Sink      ports.ToolSink
	Bus       ports.Publisher
	// NewID makes a type-prefixed ULID (store.NewID).
	NewID func(prefix string) string
	// Redact is the redaction rules (Art. 4): a call to a server off this machine whose
	// arguments they would change is refused.
	Redact func(string) string

	// BackoffBase is the first reconnection wait; zero is one second (REQ-MCP-003).
	BackoffBase time.Duration
	// CallTimeout bounds a connection and a call; zero is domain.CallTimeout.
	CallTimeout time.Duration
	// StableAfter is how long a connection must stay up to reset the reconnection count, so
	// a server that connects and drops at once spends its five attempts (REQ-MCP-003); zero
	// is a minute.
	StableAfter time.Duration
	Now         func() time.Time
	Logger      *slog.Logger
}

// Manager is ports.Servers, and the caller the registry's MCP tools run through.
type Manager struct {
	cfg  Config
	base context.Context
	stop context.CancelFunc

	mu      sync.Mutex
	closed  bool
	servers map[string]*server // by name
	wg      sync.WaitGroup
}

var _ ports.Servers = (*Manager)(nil)

// server is one configured server and its supervisor.
type server struct {
	srv     domain.Server // State, LastError and Tools change; guarded by Manager.mu
	session ports.Session
	cancel  context.CancelFunc
	done    chan struct{}
}

const prefixServer = "mcp"

// New builds the manager; Start connects the stored servers.
func New(cfg Config) (*Manager, error) {
	switch {
	case cfg.Store == nil, cfg.Connector == nil, cfg.Secrets == nil, cfg.Sink == nil, cfg.Bus == nil:
		return nil, errors.New("mcp: Store, Connector, Secrets, Sink and Bus are required")
	case cfg.NewID == nil || cfg.Redact == nil:
		return nil, errors.New("mcp: NewID and Redact are required")
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = domain.CallTimeout
	}
	if cfg.StableAfter <= 0 {
		cfg.StableAfter = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Manager{cfg: cfg, servers: map[string]*server{}}, nil
}

// Start connects every stored server (REQ-MCP-001). The connections run on a context derived
// from ctx; Close stops them.
func (m *Manager) Start(ctx context.Context) error {
	m.base, m.stop = context.WithCancel(context.WithoutCancel(ctx))
	stored, err := m.cfg.Store.List(ctx)
	if err != nil {
		return fmt.Errorf("mcp: list servers: %w", err)
	}
	for _, s := range stored {
		s.Tools = nil
		m.supervise(s) //nolint:contextcheck // a connection outlives Start: it runs on the manager's context
	}
	return nil
}

// Close disconnects every server and waits for their supervisors to end.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	if m.stop != nil {
		m.stop()
	}
	m.wg.Wait()
}

func (m *Manager) now() int64 { return m.cfg.Now().UnixMilli() }

// List is mcp.server.list: every server, by name, with its live state and tools.
func (m *Manager) List(context.Context) ([]domain.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Server, 0, len(m.servers))
	for _, s := range m.servers {
		cp := s.srv
		cp.Tools = append([]string(nil), s.srv.Tools...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Add is mcp.server.add (API §5.27): it validates the request, persists the server and starts
// connecting it. Its tools reach the registry once it connects, so a thread offers them from
// its next turn (REQ-MCP-002).
func (m *Manager) Add(ctx context.Context, p domain.AddParams) (domain.Server, error) {
	p, err := p.Validate()
	if err != nil {
		return domain.Server{}, err
	}
	if m.cfg.Redact(p.URL) != p.URL {
		return domain.Server{}, fmt.Errorf("%w: the url carries what looks like a secret; a server's url is stored in clear (REQ-SEC-004)", domain.ErrConfigInvalid)
	}
	for name, raw := range p.EnvRefs {
		ref, _ := domain.ParseRef(raw)
		if err := m.cfg.Secrets.Accepts(ctx, ref); err != nil {
			return domain.Server{}, fmt.Errorf("%w (env_refs.%s)", err, name)
		}
	}
	now := m.now()
	s := domain.Server{
		ID: m.cfg.NewID(prefixServer), Name: p.Name, Transport: p.Transport, Command: p.Command,
		Args: p.Args, URL: p.URL, EnvRefs: p.EnvRefs, Trust: p.Trust, State: domain.StateConnecting,
		CreatedAt: now, UpdatedAt: now,
	}
	m.mu.Lock()
	closed := m.closed || m.base == nil
	_, taken := m.servers[s.Name]
	m.mu.Unlock()
	switch {
	case closed:
		return domain.Server{}, errors.New("mcp: the manager is not running")
	case taken:
		return domain.Server{}, fmt.Errorf("%w: %s", domain.ErrConflict, s.Name)
	}
	if err := m.cfg.Store.Insert(ctx, s); err != nil {
		return domain.Server{}, err
	}
	if !m.supervise(s) { //nolint:contextcheck // a connection outlives the request that added it
		return domain.Server{}, errors.New("mcp: the manager closed; the server connects at the next start")
	}
	return s, nil
}

// Remove is mcp.server.remove: the server is disconnected, its tools leave the registry, and
// its row is deleted.
func (m *Manager) Remove(ctx context.Context, name string) error {
	m.mu.Lock()
	s, ok := m.servers[name]
	if ok {
		delete(m.servers, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, name)
	}
	s.cancel()
	<-s.done
	m.cfg.Sink.Drop(name)
	return m.cfg.Store.Delete(ctx, name)
}

// Call runs one tool of a connected server. A call the server does not answer within the
// timeout fails and marks the server unavailable, which closes its session and starts the
// reconnection (REQ-MCP-003). A call the caller cancels is not the server's fault.
func (m *Manager) Call(ctx context.Context, name, tool string, args json.RawMessage) (domain.CallResult, error) {
	m.mu.Lock()
	s, ok := m.servers[name]
	var session ports.Session
	var srv domain.Server
	if ok {
		session, srv = s.session, s.srv
	}
	m.mu.Unlock()
	if !ok || session == nil {
		return domain.CallResult{}, fmt.Errorf("%w: %s", domain.ErrUnavailable, name)
	}
	if domain.Remote(srv) && m.cfg.Redact(string(args)) != string(args) {
		return domain.CallResult{}, fmt.Errorf("%w: %s is off this machine and the arguments carry what looks like a secret", domain.ErrRefused, name)
	}

	callCtx, cancel := context.WithTimeout(ctx, m.cfg.CallTimeout)
	defer cancel()
	res, err := session.Call(callCtx, tool, args)
	if err != nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		reason := fmt.Sprintf("the server did not respond within %v", m.cfg.CallTimeout)
		m.lost(ctx, s, session, reason)
		return domain.CallResult{}, fmt.Errorf("%w: %s: %s", domain.ErrUnavailable, name, reason)
	}
	return res, err
}

// lost marks a server unavailable for reason and closes its session; the supervisor then
// reconnects it.
func (m *Manager) lost(ctx context.Context, s *server, session ports.Session, reason string) {
	m.mu.Lock()
	current := s.session == session
	if current {
		s.session = nil
	}
	m.mu.Unlock()
	if !current {
		return
	}
	m.setState(ctx, s, domain.StateUnavailable, reason)
	_ = session.Close()
}

// supervise registers the server and runs its connection loop until it is removed or the
// manager closes. A closed manager starts nothing, and it reports false.
func (m *Manager) supervise(srv domain.Server) bool {
	ctx, cancel := context.WithCancel(m.base)
	s := &server{srv: srv, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return false
	}
	m.servers[srv.Name] = s
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer close(s.done)
		defer cancel()
		m.run(ctx, s)
	}()
	return true
}

// run connects the server and, each time the connection is lost or fails, waits and tries
// again: BackoffBase, then doubled, MaxReconnects times; after that the server stays
// unavailable (REQ-MCP-003). A connection that stays up StableAfter resets the count; one that
// drops sooner spends an attempt like one that never connected.
func (m *Manager) run(ctx context.Context, s *server) {
	attempt := 0
	for {
		session, err := m.connect(ctx, s)
		if ctx.Err() != nil {
			if session != nil {
				_ = session.Close()
			}
			return
		}
		if err == nil {
			up := time.Now()
			select {
			case <-session.Done():
				if time.Since(up) >= m.cfg.StableAfter {
					attempt = 0
				}
				m.lost(ctx, s, session, "the connection ended")
			case <-ctx.Done():
				_ = session.Close()
				return
			}
		} else {
			m.setState(ctx, s, domain.StateUnavailable, err.Error())
		}
		if attempt >= domain.MaxReconnects {
			m.cfg.Logger.Warn("an MCP server stays unavailable after its last reconnection attempt",
				slog.String("server", s.srv.Name), slog.Int("attempts", attempt))
			return
		}
		select {
		case <-time.After(domain.Backoff(m.cfg.BackoffBase, attempt)):
		case <-ctx.Done():
			return
		}
		attempt++
	}
}

// connect opens a session, lists its tools and hands them to the registry, within the call
// timeout, and marks the server connected.
func (m *Manager) connect(ctx context.Context, s *server) (ports.Session, error) {
	m.mu.Lock()
	srv := s.srv
	m.mu.Unlock()
	env, degraded, err := m.environment(ctx, srv)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, m.cfg.CallTimeout)
	defer cancel()
	session, err := m.cfg.Connector.Connect(cctx, srv, env)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	tools, err := session.Tools(cctx)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("list tools: %w", err)
	}
	skipped := m.cfg.Sink.Sync(srv, tools)
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if _, bad := skipped[t.Name]; !bad {
			names = append(names, t.Name)
		}
	}
	m.mu.Lock()
	s.session, s.srv.Tools = session, names
	m.mu.Unlock()
	m.setState(ctx, s, domain.StateConnected, joinNotes(degraded, skippedNote(skipped)))
	return session, nil
}

// environment resolves a stdio server's env_refs, and notes the ones resolved in a degraded
// mode (REQ-SEC-012). A reference that cannot be resolved is the connection's error, named by
// variable and reason, never by value.
func (m *Manager) environment(ctx context.Context, srv domain.Server) (map[string]string, string, error) {
	env := map[string]string{}
	var degraded []string
	for name, raw := range srv.EnvRefs {
		ref, err := domain.ParseRef(raw)
		if err != nil {
			return nil, "", fmt.Errorf("env_refs.%s: %w", name, err)
		}
		value, why, err := m.cfg.Secrets.Resolve(ctx, ref)
		if err != nil {
			return nil, "", fmt.Errorf("env_refs.%s: %w", name, err)
		}
		if why != "" {
			degraded = append(degraded, name+": "+why)
		}
		env[name] = value
	}
	if len(degraded) == 0 {
		return env, "", nil
	}
	sort.Strings(degraded)
	return env, "degraded: " + strings.Join(degraded, "; "), nil
}

// joinNotes is last_error for a connected server: what is degraded, then what was skipped.
func joinNotes(notes ...string) string {
	var kept []string
	for _, n := range notes {
		if n != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, ". ")
}

// skippedNote is last_error for a connected server some of whose tools were not registered.
func skippedNote(skipped map[string]string) string {
	if len(skipped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(skipped))
	for name, why := range skipped {
		parts = append(parts, name+": "+why)
	}
	sort.Strings(parts)
	return "tools not offered: " + strings.Join(parts, "; ")
}

// setState persists a server's state, then publishes it (DD-007), on a context the caller's
// cancellation cannot stop. A write that fails is logged and the event still goes out: the
// live state is the manager's.
func (m *Manager) setState(ctx context.Context, s *server, state domain.State, reason string) {
	m.mu.Lock()
	s.srv.State, s.srv.LastError = state, reason
	srv := s.srv
	m.mu.Unlock()
	if err := m.cfg.Store.SetState(context.WithoutCancel(ctx), srv.ID, state, reason, m.now()); err != nil {
		m.cfg.Logger.Error("an MCP server's state could not be written", slog.String("server", srv.Name), slog.Any("error", err))
	}
	m.cfg.Bus.Publish(ports.ServerState{Name: srv.Name, State: state, LastError: reason})
}
