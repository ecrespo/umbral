package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/remote/domain"
	"github.com/ecrespo/umbral/internal/remote/ports"
)

type memStore struct {
	mu    sync.Mutex
	hosts map[string]domain.Host
}

func (s *memStore) List(context.Context) ([]domain.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, h)
	}
	return out, nil
}

func (s *memStore) Insert(_ context.Context, h domain.Host) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[h.Name]; ok {
		return domain.ErrConflict
	}
	s.hosts[h.Name] = h
	return nil
}

func (s *memStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[name]; !ok {
		return domain.ErrNotFound
	}
	delete(s.hosts, name)
	return nil
}

type fakeForward struct {
	done chan struct{}
	once sync.Once
}

func (f *fakeForward) Done() <-chan struct{} { return f.done }
func (f *fakeForward) Err() error            { return errors.New("ssh exited") }
func (f *fakeForward) Close() error          { f.once.Do(func() { close(f.done) }); return nil }

// fakeTunnels forwards nothing: it answers an endpoint, creates the local socket file and hands
// the test each forward so it can end it.
type fakeTunnels struct {
	mu        sync.Mutex
	endpoints int
	failNext  int
	forwards  chan *fakeForward
}

func (f *fakeTunnels) Endpoint(_ context.Context, t domain.Target) (domain.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints++
	if f.failNext > 0 {
		f.failNext--
		return domain.Endpoint{}, errors.New("ssh: connect to host " + t.Host + ": Connection refused")
	}
	return domain.Endpoint{Socket: "/run/remote/umbral.sock", Token: "remote-token"}, nil
}

func (f *fakeTunnels) Forward(_ context.Context, _ domain.Target, local, remote string) (ports.Forward, error) {
	if remote != "/run/remote/umbral.sock" {
		return nil, errors.New("forwarded the wrong socket: " + remote)
	}
	if err := os.WriteFile(local, nil, 0o600); err != nil {
		return nil, err
	}
	fw := &fakeForward{done: make(chan struct{})}
	f.forwards <- fw
	return fw, nil
}

type recorder struct {
	mu     sync.Mutex
	events []ports.HostState
	wake   chan struct{}
}

func (r *recorder) Publish(ev bus.Event) {
	if s, ok := ev.(ports.HostState); ok {
		r.mu.Lock()
		r.events = append(r.events, s)
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// waitState waits for the host to be published in state.
func (r *recorder) waitState(t *testing.T, name string, state domain.State) ports.HostState {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		for i, ev := range r.events {
			if ev.Name == name && ev.State == state {
				r.events = r.events[i+1:]
				r.mu.Unlock()
				return ev
			}
		}
		r.mu.Unlock()
		select {
		case <-r.wake:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("%s never became %s", name, state)
		}
	}
}

func newManager(t *testing.T, tun *fakeTunnels, probe ...func(context.Context, ports.Link) error) (*Manager, *recorder, *memStore) {
	t.Helper()
	st := &memStore{hosts: map[string]domain.Host{}}
	rec := &recorder{wake: make(chan struct{}, 1)}
	m, err := New(Options{
		Store: st, Tunnels: tun, Publisher: rec, Dir: filepath.Join(socketDir(t), "remotes"),
		Backoff: func(int) time.Duration { return 10 * time.Millisecond },
	})
	if len(probe) > 0 {
		m.opts.Probe = probe[0]
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); m.Close() })
	return m, rec, st
}

func TestAnAddedHostIsLinkedThroughItsForwardedSocket_REQ_RMT_001(t *testing.T) {
	tun := &fakeTunnels{forwards: make(chan *fakeForward, 4)}
	m, rec, st := newManager(t, tun)

	got, err := m.Add(t.Context(), "nas", "ssh://me@nas")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateConnecting || got.Host.Name != "nas" {
		t.Fatalf("Add = %+v", got)
	}
	if _, ok := st.hosts["nas"]; !ok {
		t.Fatal("the host was not stored")
	}
	rec.waitState(t, "nas", domain.StateConnected)
	link, ok := m.Link("nas")
	if !ok || link.Token != "remote-token" || filepath.Base(link.Socket) != socketName("nas", os.Getpid()) {
		t.Fatalf("Link = %+v, %v", link, ok)
	}
	info, err := os.Stat(filepath.Dir(link.Socket))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the sockets' directory: %v, %v", info, err)
	}

	for _, bad := range [][2]string{{"Nas", "ssh://nas"}, {"box", "tcp://box:1"}} {
		if _, err := m.Add(t.Context(), bad[0], bad[1]); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("Add(%q, %q) = %v, want ErrValidation", bad[0], bad[1], err)
		}
	}
	if _, err := m.Add(t.Context(), "nas", "ssh://nas"); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("a second nas = %v, want ErrConflict", err)
	}
}

func TestADroppedLinkIsRetriedAndOthersKeepTheirs_REQ_RMT_002(t *testing.T) {
	tun := &fakeTunnels{forwards: make(chan *fakeForward, 8)}
	m, rec, _ := newManager(t, tun)
	if _, err := m.Add(t.Context(), "nas", "ssh://nas"); err != nil {
		t.Fatal(err)
	}
	nas := <-tun.forwards
	rec.waitState(t, "nas", domain.StateConnected)
	if _, err := m.Add(t.Context(), "box", "ssh://box"); err != nil {
		t.Fatal(err)
	}
	<-tun.forwards
	rec.waitState(t, "box", domain.StateConnected)

	// nas's link drops, and its first reconnection fails too.
	tun.mu.Lock()
	tun.failNext = 1
	tun.mu.Unlock()
	_ = nas.Close()
	ev := rec.waitState(t, "nas", domain.StateDisconnected)
	if ev.LastError == "" {
		t.Error("a dropped link carries no reason")
	}
	if _, ok := m.Link("nas"); ok {
		t.Error("a disconnected host still offers its link")
	}
	if _, ok := m.Link("box"); !ok {
		t.Error("box lost its link when nas's dropped")
	}
	ev = rec.waitState(t, "nas", domain.StateDisconnected)
	if ev.LastError == "" {
		t.Error("a failed reconnection carries no reason")
	}
	<-tun.forwards
	rec.waitState(t, "nas", domain.StateConnected)
	if _, ok := m.Link("nas"); !ok {
		t.Fatal("nas did not come back")
	}
	list, err := m.List(t.Context())
	if err != nil || len(list) != 2 || list[0].Host.Name != "box" || list[1].State != domain.StateConnected {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestARemovedHostIsUnlinked_REQ_RMT_001(t *testing.T) {
	tun := &fakeTunnels{forwards: make(chan *fakeForward, 4)}
	m, rec, st := newManager(t, tun)
	if _, err := m.Add(t.Context(), "nas", "ssh://nas"); err != nil {
		t.Fatal(err)
	}
	fw := <-tun.forwards
	rec.waitState(t, "nas", domain.StateConnected)
	link, _ := m.Link("nas")

	if err := m.Remove(t.Context(), "nas"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fw.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the forward outlived its host")
	}
	if _, ok := st.hosts["nas"]; ok || m.Known("nas") {
		t.Fatal("the host is still configured")
	}
	if _, err := os.Stat(link.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the forwarded socket is left behind: %v", err)
	}
	if err := m.Remove(t.Context(), "nas"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a second remove = %v, want ErrNotFound", err)
	}
}

func TestStartLinksTheStoredHosts_REQ_RMT_001(t *testing.T) {
	tun := &fakeTunnels{forwards: make(chan *fakeForward, 4)}
	st := &memStore{hosts: map[string]domain.Host{"nas": {Name: "nas", URL: "ssh://nas"}}}
	rec := &recorder{wake: make(chan struct{}, 1)}
	m, err := New(Options{
		Store: st, Tunnels: tun, Publisher: rec, Dir: filepath.Join(t.TempDir(), "r"),
		Backoff: func(int) time.Duration { return 10 * time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	fw := <-tun.forwards
	rec.waitState(t, "nas", domain.StateConnected)
	cancel()
	m.Close()
	select {
	case <-fw.Done():
	default:
		t.Fatal("Close left a forward running")
	}
}

func TestStartSweepsTheSocketsOfADaemonThatIsGone_REQ_RMT_002(t *testing.T) {
	dir := t.TempDir()
	// A pid past any the kernel hands out is a daemon that is gone; this process is one that is not.
	gone := filepath.Join(dir, socketName("nas", 1<<30))
	mine := filepath.Join(dir, socketName("nas", os.Getpid()))
	other := filepath.Join(dir, "notes.txt")
	for _, f := range []string{gone, mine, other} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(Options{Store: &memStore{hosts: map[string]domain.Host{}}, Tunnels: &fakeTunnels{}, Publisher: &recorder{}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a gone daemon's socket was kept: %v", err)
	}
	for _, f := range []string{mine, other} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was removed: %v", f, err)
		}
	}
}

// TestAHostIsConnectedOnlyOnceItsDaemonAnswers_REQ_RMT_002: a forward that started says nothing
// of the remote daemon, so the host stays out of `connected` until the probe passes, and a
// failing probe is retried like a dropped link.
func TestAHostIsConnectedOnlyOnceItsDaemonAnswers_REQ_RMT_002(t *testing.T) {
	tun := &fakeTunnels{forwards: make(chan *fakeForward, 8)}
	var mu sync.Mutex
	fails := 2
	m, rec, _ := newManager(t, tun, func(_ context.Context, link ports.Link) error {
		if link.Token != "remote-token" {
			return errors.New("probed with the wrong token")
		}
		mu.Lock()
		defer mu.Unlock()
		if fails > 0 {
			fails--
			return errors.New("connection reset by peer")
		}
		return nil
	})
	if _, err := m.Add(t.Context(), "nas", "ssh://nas"); err != nil {
		t.Fatal(err)
	}
	down := rec.waitState(t, "nas", domain.StateDisconnected)
	if down.LastError == "" {
		t.Fatal("a daemon that does not answer left no reason")
	}
	if _, ok := m.Link("nas"); ok {
		mu.Lock()
		left := fails
		mu.Unlock()
		if left > 0 {
			t.Fatal("the host is linked before its daemon answered")
		}
	}
	rec.waitState(t, "nas", domain.StateConnected)
	if _, ok := m.Link("nas"); !ok {
		t.Fatal("the host is not linked once its daemon answers")
	}
}

// TestANameWhoseSocketCannotBeBoundIsRefused_REQ_RMT_001: the forwarded socket's path must fit
// every supported OS's limit, or ssh fails to bind it on every attempt.
func TestANameWhoseSocketCannotBeBoundIsRefused_REQ_RMT_001(t *testing.T) {
	tun := &fakeTunnels{forwards: make(chan *fakeForward, 4)}
	m, _, _ := newManager(t, tun)
	m.opts.Dir = "/" + strings.Repeat("d", 80)
	if _, err := m.Add(t.Context(), "a-host-name-thirty-two-bytes-xyz", "ssh://nas"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a socket path past the limit = %v, want ErrValidation", err)
	}
}
