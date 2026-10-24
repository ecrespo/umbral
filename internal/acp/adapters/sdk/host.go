// Package sdk hosts ACP agents on github.com/coder/acp-go-sdk (REQ-ACP-001, 002, 006, 009):
// one process per thread, launched through its manifest in an isolated home with an
// allowlisted environment, spoken to over stdio, and ended when it exits, stops speaking ACP
// or falls silent.
package sdk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

// Config wires the host. Dir and Secrets are required.
type Config struct {
	// Entries are `[[acp.agents]]`, read at start.
	Entries []domain.Entry
	// Manifests are the manifests an entry may name; nil is domain.Builtin().
	Manifests map[string]domain.Manifest
	// Dir is where each thread's isolated home is made: `acp/` in the daemon's runtime
	// directory, so nothing of it outlives the machine's session.
	Dir     string
	Secrets ports.Secrets
	// Mediator answers the agent's requests of Umbral; nil refuses every one of them.
	Mediator ports.Mediator
	// Pending reports whether an approval of the thread is pending, which pauses the silence
	// timer (REQ-ACP-006); nil is never.
	Pending func(threadID string) bool
	// SilenceTimeout is `[acp] silence_timeout`; zero is domain.DefaultSilenceTimeout.
	SilenceTimeout time.Duration
	// KillAfter is how long a cancelled turn may take to end before the process is killed
	// (REQ-ACP-008); zero is five seconds.
	KillAfter time.Duration
	// Environ is the daemon's environment, which the agent's is filtered from; nil is
	// os.Environ.
	Environ func() []string
	Logger  *slog.Logger
}

// Request names a thread's agent and where it runs: the thread's worktree, or its directory
// without one (REQ-ACP-001).
type Request struct {
	ThreadID string
	Agent    string
	Dir      string
}

// Host is the daemon's ACP client.
type Host struct {
	cfg     Config
	entries map[string]domain.Entry

	mu     sync.Mutex
	closed bool
	agents map[string]*process // thread id → its running agent
}

// New builds a host.
func New(cfg Config) (*Host, error) {
	if cfg.Dir == "" {
		return nil, errors.New("acp: a Dir is required")
	}
	if cfg.Secrets == nil {
		return nil, errors.New("acp: Secrets are required")
	}
	if cfg.Manifests == nil {
		cfg.Manifests = domain.Builtin()
	}
	if cfg.Mediator == nil {
		cfg.Mediator = Refuse{}
	}
	if cfg.Pending == nil {
		cfg.Pending = func(string) bool { return false }
	}
	if cfg.SilenceTimeout <= 0 {
		cfg.SilenceTimeout = domain.DefaultSilenceTimeout
	}
	if cfg.KillAfter <= 0 {
		cfg.KillAfter = 5 * time.Second
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	h := &Host{cfg: cfg, entries: map[string]domain.Entry{}, agents: map[string]*process{}}
	for _, e := range cfg.Entries {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("acp: %w", err)
		}
		if _, dup := h.entries[e.Name]; dup {
			return nil, fmt.Errorf("acp: agent %q is listed twice", e.Name)
		}
		h.entries[e.Name] = e
	}
	return h, nil
}

// Refuse is the mediator that allows nothing.
type Refuse struct{}

// Permission refuses.
func (Refuse) Permission(context.Context, ports.PermissionRequest) (bool, error) { return false, nil }

// ReadFile refuses.
func (Refuse) ReadFile(context.Context, string, string) (string, error) {
	return "", errors.New("umbral does not serve file reads to this agent")
}

// WriteFile refuses.
func (Refuse) WriteFile(context.Context, string, string, string) error {
	return errors.New("umbral does not serve file writes to this agent")
}

// lookup is an entry and its manifest, or why there is none.
func (h *Host) lookup(agent string) (domain.Entry, domain.Manifest, error) {
	e, ok := h.entries[agent]
	if !ok {
		return domain.Entry{}, domain.Manifest{}, &domain.Refusal{
			Reason: domain.ReasonUnknownAgent,
			Detail: fmt.Sprintf("agent %q is not an [[acp.agents]] entry of config.toml", agent),
		}
	}
	m, ok := h.cfg.Manifests[e.ManifestName()]
	if !ok {
		return domain.Entry{}, domain.Manifest{}, &domain.Refusal{
			Reason: domain.ReasonNoManifest,
			Detail: fmt.Sprintf("agent %q has no manifest that makes it ask before every side effect (manifest %q is unknown)",
				agent, e.ManifestName()),
		}
	}
	return e, m, nil
}

// Check refuses an agent that may not run in dir (REQ-ACP-009): no such entry, no manifest, a
// key of its config the manifest does not allow, or a repository configuration of the agent
// in dir or above it.
func (h *Host) Check(agent, dir string) error {
	e, m, err := h.lookup(agent)
	if err != nil {
		return err
	}
	if _, err := m.Generate(e.Config); err != nil {
		return err
	}
	found, err := findProjectConfig(m, dir)
	if err != nil {
		return err
	}
	if found != "" {
		return &domain.Refusal{
			Reason: domain.ReasonProjectConfig,
			Detail: fmt.Sprintf("%s configures the agent from the repository, after Umbral's configuration; remove it or create the thread elsewhere", found),
		}
	}
	return nil
}

// findProjectConfig returns the first of the manifest's project configuration entries found in
// dir or a directory above it, up to the repository's top — the nearest directory holding a
// `.git` — or, outside a repository, up to the root. "" is none.
func findProjectConfig(m domain.Manifest, dir string) (string, error) {
	dir = filepath.Clean(dir)
	for {
		for _, name := range m.ProjectConfig {
			p := filepath.Join(dir, name)
			_, err := os.Lstat(p)
			if err == nil {
				return p, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("acp: look for %s: %w", p, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// Open launches the thread's agent and opens its session, if it is not running already.
func (h *Host) Open(ctx context.Context, req Request) error {
	_, err := h.ensure(ctx, req)
	return err
}

// ensure is the thread's running agent in req.Dir, launched now if there is none: its process
// ended, or it never started in this daemon run. A relaunch starts a new ACP session.
func (h *Host) ensure(ctx context.Context, req Request) (*process, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("acp: the host is closed")
	}
	old := h.agents[req.ThreadID]
	if old != nil && old.alive() && old.dir == req.Dir && old.agent == req.Agent {
		h.mu.Unlock()
		return old, nil
	}
	delete(h.agents, req.ThreadID)
	h.mu.Unlock()
	if old != nil {
		old.kill()
	}

	p, err := h.launch(ctx, req)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		p.kill()
		return nil, errors.New("acp: the host is closed")
	}
	h.agents[req.ThreadID] = p
	return p, nil
}

// launch starts the agent through its manifest (REQ-ACP-009), then `initialize` and
// `session/new` (REQ-ACP-001). The checks run again at every launch: the agent itself could
// have written a repository configuration since the last one.
func (h *Host) launch(ctx context.Context, req Request) (*process, error) {
	if err := h.Check(req.Agent, req.Dir); err != nil {
		return nil, err
	}
	entry, m, _ := h.lookup(req.Agent)
	generated, err := m.Generate(entry.Config)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for name, raw := range entry.EnvRefs {
		value, err := h.cfg.Secrets.Resolve(ctx, raw)
		if err != nil {
			return nil, fmt.Errorf("acp: agent %q: env_refs.%s: %w", entry.Name, name, err)
		}
		refs[name] = value
	}

	iso := domain.Isolation{Root: filepath.Join(h.cfg.Dir, req.ThreadID)}
	if err := os.RemoveAll(iso.Root); err != nil {
		return nil, fmt.Errorf("acp: clear the agent's home: %w", err)
	}
	for _, d := range iso.Dirs() {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("acp: make the agent's home: %w", err)
		}
	}
	configPath := filepath.Join(iso.Config(), m.ConfigPath)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, fmt.Errorf("acp: make the agent's config directory: %w", err)
	}
	if err := os.WriteFile(configPath, generated, 0o600); err != nil {
		return nil, fmt.Errorf("acp: write the agent's config: %w", err)
	}

	// Not CommandContext: the process outlives the request that launched it, and ends by kill.
	cmd := exec.Command(entry.Command, entry.Args...) //nolint:noctx,gosec // outlives the request; the user's own [[acp.agents]] entry
	cmd.Dir = req.Dir
	cmd.Env = domain.Environment(h.cfg.Environ(), iso, m, refs)
	// Its own process group, so the shells and servers it starts end with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p := newProcess(h, req, iso)
	if err := p.start(cmd); err != nil {
		_ = os.RemoveAll(iso.Root)
		return nil, fmt.Errorf("%w: launch %s: %w", domain.ErrAgent, entry.Name, err)
	}

	hctx, cancel := context.WithTimeout(ctx, h.cfg.SilenceTimeout)
	defer cancel()
	err = p.call(hctx, func(ctx context.Context) error {
		_, err := p.conn.Initialize(ctx, acp.InitializeRequest{
			ProtocolVersion: acp.ProtocolVersionNumber,
			ClientCapabilities: acp.ClientCapabilities{
				Fs:       acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
				Terminal: true,
			},
		})
		if err != nil {
			return fmt.Errorf("initialize: %w", err)
		}
		sess, err := p.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: req.Dir, McpServers: []acp.McpServer{}})
		if err != nil {
			return fmt.Errorf("session/new: %w", err)
		}
		p.session = sess.SessionId
		return nil
	})
	if err != nil {
		p.kill()
		return nil, err
	}
	h.cfg.Logger.InfoContext(ctx, "acp agent started", slog.String("thread", req.ThreadID), slog.String("agent", req.Agent),
		slog.Int("pid", cmd.Process.Pid))
	return p, nil
}

// Close stops the thread's agent, if it runs.
func (h *Host) Close(threadID string) {
	h.mu.Lock()
	p := h.agents[threadID]
	delete(h.agents, threadID)
	h.mu.Unlock()
	if p != nil {
		p.kill()
	}
}

// Shutdown stops every agent; the host launches none after it.
func (h *Host) Shutdown() {
	h.mu.Lock()
	h.closed = true
	all := h.agents
	h.agents = map[string]*process{}
	h.mu.Unlock()
	for _, p := range all {
		p.kill()
	}
}

// Running reports whether the thread's agent process is running, for tests and status.
func (h *Host) Running(threadID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.agents[threadID]
	return p != nil && p.alive()
}
