package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/mcp/ports"
)

// memStore is ports.Store in memory.
type memStore struct {
	mu      sync.Mutex
	servers map[string]domain.Server
}

func (s *memStore) List(context.Context) ([]domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Server, 0, len(s.servers))
	for _, v := range s.servers {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memStore) Insert(_ context.Context, srv domain.Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[srv.Name]; ok {
		return domain.ErrConflict
	}
	s.servers[srv.Name] = srv
	return nil
}

func (s *memStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[name]; !ok {
		return domain.ErrNotFound
	}
	delete(s.servers, name)
	return nil
}

func (s *memStore) SetState(_ context.Context, id string, state domain.State, lastError string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.servers {
		if v.ID == id {
			v.State, v.LastError, v.UpdatedAt = state, lastError, now
			s.servers[k] = v
			return nil
		}
	}
	return domain.ErrNotFound
}

func (s *memStore) state(name string) domain.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.servers[name].State
}

// fakeSession is one connection: its tools, a call function, and a done channel the test can
// close to simulate a crash.
type fakeSession struct {
	tools    []domain.ToolInfo
	call     func(ctx context.Context, tool string, args json.RawMessage) (domain.CallResult, error)
	done     chan struct{}
	once     sync.Once
	closed   atomic.Bool
	received atomic.Int32
}

func (s *fakeSession) Tools(context.Context) ([]domain.ToolInfo, error) { return s.tools, nil }

func (s *fakeSession) Call(ctx context.Context, tool string, args json.RawMessage) (domain.CallResult, error) {
	s.received.Add(1)
	if s.call != nil {
		return s.call(ctx, tool, args)
	}
	select {
	case <-s.done:
		return domain.CallResult{}, errors.New("connection closed")
	default:
	}
	return domain.CallResult{Text: tool + " " + string(args)}, nil
}

func (s *fakeSession) Done() <-chan struct{} { return s.done }

func (s *fakeSession) crash() { s.once.Do(func() { close(s.done) }) }

func (s *fakeSession) Close() error {
	s.closed.Store(true)
	s.crash()
	return nil
}

// fakeConnector hands out sessions from newSession, and fails the attempts listed in fail.
type fakeConnector struct {
	mu       sync.Mutex
	attempts int
	fail     func(attempt int) error
	sessions []*fakeSession
	envs     []map[string]string
	tools    []domain.ToolInfo
	call     func(ctx context.Context, tool string, args json.RawMessage) (domain.CallResult, error)
}

func (c *fakeConnector) Connect(_ context.Context, _ domain.Server, env map[string]string) (ports.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts++
	c.envs = append(c.envs, env)
	if c.fail != nil {
		if err := c.fail(c.attempts); err != nil {
			return nil, err
		}
	}
	s := &fakeSession{tools: c.tools, call: c.call, done: make(chan struct{})}
	c.sessions = append(c.sessions, s)
	return s, nil
}

func (c *fakeConnector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

func (c *fakeConnector) last() *fakeSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sessions) == 0 {
		return nil
	}
	return c.sessions[len(c.sessions)-1]
}

// fakeSecrets resolves keyring:<path> from a map and refuses env: refs unless allowEnv.
type fakeSecrets struct {
	values   map[string]string
	allowEnv bool
}

func (f fakeSecrets) Accepts(_ context.Context, r domain.Ref) error {
	if r.Kind == domain.RefEnv && !f.allowEnv {
		return fmt.Errorf("%w: env references need the keyring fallback", domain.ErrConfigInvalid)
	}
	return nil
}

func (f fakeSecrets) Resolve(_ context.Context, r domain.Ref) (string, string, error) {
	v, ok := f.values[r.Name]
	if !ok {
		return "", "", errors.New("secret_not_found")
	}
	if r.Kind == domain.RefEnv {
		return v, "env_secret", nil
	}
	return v, "", nil
}

// fakeSink records what the registry was told.
type fakeSink struct {
	mu     sync.Mutex
	tools  map[string][]string
	synced chan string
}

func (s *fakeSink) Sync(srv domain.Server, tools []domain.ToolInfo) map[string]string {
	s.mu.Lock()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	s.tools[srv.Name] = names
	s.mu.Unlock()
	if s.synced != nil {
		s.synced <- srv.Name
	}
	return nil
}

func (s *fakeSink) Drop(server string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tools, server)
}

func (s *fakeSink) has(server string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tools[server]
	return ok
}

// stateBus records mcp.server_state events and checks each was persisted first (DD-007).
type stateBus struct {
	t      *testing.T
	store  *memStore
	mu     sync.Mutex
	events []ports.ServerState
	ch     chan ports.ServerState
}

func (b *stateBus) Publish(ev bus.Event) {
	e, ok := ev.(ports.ServerState)
	if !ok {
		return
	}
	if got := b.store.state(e.Name); got != e.State {
		b.t.Errorf("mcp.server_state %s %s published while the store says %s", e.Name, e.State, got)
	}
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
	b.ch <- e
}

type rig struct {
	m     *Manager
	store *memStore
	conn  *fakeConnector
	sink  *fakeSink
	bus   *stateBus
}

var ids atomic.Int64

func newRig(t *testing.T, conn *fakeConnector, secrets fakeSecrets, seed ...domain.Server) *rig {
	t.Helper()
	store := &memStore{servers: map[string]domain.Server{}}
	for _, s := range seed {
		store.servers[s.Name] = s
	}
	sink := &fakeSink{tools: map[string][]string{}}
	b := &stateBus{t: t, store: store, ch: make(chan ports.ServerState, 64)}
	m, err := New(Config{
		Store: store, Connector: conn, Secrets: secrets, Sink: sink, Bus: b,
		NewID:       func(p string) string { return fmt.Sprintf("%s_%026d", p, ids.Add(1)) },
		Redact:      func(s string) string { return strings.ReplaceAll(s, "ghp_secret", "[REDACTED]") },
		BackoffBase: 10 * time.Millisecond,
		CallTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return &rig{m: m, store: store, conn: conn, sink: sink, bus: b}
}

// await waits for the server to reach state.
func (r *rig) await(t *testing.T, name string, state domain.State) ports.ServerState {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-r.bus.ch:
			if e.Name == name && e.State == state {
				return e
			}
		case <-deadline:
			t.Fatalf("%s never reached %s", name, state)
		}
	}
}
