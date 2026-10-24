package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/acp/adapters/sdk/acptest"
	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

func TestMain(m *testing.M) {
	acptest.Serve()
	os.Exit(m.Run())
}

// sink records one turn's reports.
type sink struct {
	mu       sync.Mutex
	text     strings.Builder
	thoughts []string
	calls    []domain.ToolCall
	fail     error
}

func (s *sink) Text(t string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.text.WriteString(t)
	return nil
}

func (s *sink) Thought(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thoughts = append(s.thoughts, t)
}

func (s *sink) ToolCall(c domain.ToolCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	return nil
}

func (s *sink) Text0() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
}

type secrets map[string]string

func (s secrets) Resolve(_ context.Context, ref string) (string, error) {
	v, ok := s[ref]
	if !ok {
		return "", errors.New("no such secret")
	}
	return v, nil
}

// fakeEntry is an [[acp.agents]] entry that launches this test binary as the agent, through
// the OpenCode manifest.
func fakeEntry(t *testing.T) domain.Entry {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return domain.Entry{Name: "fake", Manifest: domain.ManifestOpenCode, Command: exe, Args: acptest.Args()}
}

func newHost(t *testing.T, mutate func(*Config)) *Host {
	t.Helper()
	cfg := Config{
		Entries: []domain.Entry{fakeEntry(t)},
		Dir:     filepath.Join(t.TempDir(), "acp"),
		Secrets: secrets{},
		Environ: func() []string { return []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"} },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Shutdown)
	return h
}

func workDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A repository's top, so the search for a project configuration stops here.
	gitDir(t, filepath.Join(dir, ".git"))
	return dir
}

// gitDir makes a directory git takes for a repository's: HEAD, objects and refs.
func gitDir(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func prompt(t *testing.T, h *Host, dir, text string) (*sink, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s := &sink{}
	stop, err := h.Prompt(ctx, Request{ThreadID: "thr_1", Agent: "fake", Dir: dir}, text, s)
	return s, stop, err
}

func report(t *testing.T, h *Host, dir string) acptest.Report {
	t.Helper()
	s, stop, err := prompt(t, h, dir, "report")
	if err != nil || stop != "end_turn" {
		t.Fatalf("report: stop %q, err %v", stop, err)
	}
	var r acptest.Report
	if err := json.Unmarshal([]byte(s.Text0()), &r); err != nil {
		t.Fatalf("report %q: %v", s.Text0(), err)
	}
	return r
}

// TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006: the agent exits part way through a turn. The
// turn ends with the agent's error, what it streamed before is kept, and the next prompt
// relaunches it.
func TestAnACPAgentCrashEndsTheTurn_REQ_ACP_006(t *testing.T) {
	t.Parallel()
	h := newHost(t, nil)
	dir := workDir(t)

	s, _, err := prompt(t, h, dir, "crash")
	if !errors.Is(err, domain.ErrAgent) || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("a crash ended the turn with %v, want the agent's error naming the exit", err)
	}
	// Its error text is logged and sent to clients; the agent's stderr is not in it (Art. 7).
	if strings.Contains(err.Error(), acptest.StderrSecret) {
		t.Errorf("the agent's error carries its stderr: %v", err)
	}
	if s.Text0() != "partial" {
		t.Errorf("the turn kept %q, want what the agent streamed before it crashed", s.Text0())
	}
	if h.Running("thr_1") {
		t.Error("the crashed agent is still listed as running")
	}

	s, stop, err := prompt(t, h, dir, "again")
	if err != nil || stop != "end_turn" || s.Text0() != "echo: again" {
		t.Fatalf("the next prompt: %q, stop %q, err %v; want the agent relaunched", s.Text0(), stop, err)
	}
}

// TestAnACPAgentsErrorTextIsNotRepeated_REQ_ACP_006: an agent that answers its prompt with an
// error ends the turn `agent_error`; the error's message and data are the agent's to write, so
// only its code is in what is logged and sent to clients (Art. 7).
func TestAnACPAgentsErrorTextIsNotRepeated_REQ_ACP_006(t *testing.T) {
	t.Parallel()
	logs := &lockedBuilder{b: &strings.Builder{}}
	h := newHost(t, func(c *Config) { c.Logger = slog.New(slog.NewTextHandler(logs, nil)) })
	_, _, err := prompt(t, h, workDir(t), "fail")
	if !errors.Is(err, domain.ErrAgent) || !strings.Contains(err.Error(), "-32001") {
		t.Fatalf("an error answer ended the turn with %v, want the agent's error with its code", err)
	}
	if strings.Contains(err.Error(), acptest.StderrSecret) {
		t.Errorf("the turn's error repeats the agent's text: %v", err)
	}
	if strings.Contains(logs.String(), acptest.StderrSecret) {
		t.Errorf("the log repeats the agent's text: %s", logs.String())
	}
}

// lockedBuilder is a strings.Builder safe for a logger's concurrent writes.
type lockedBuilder struct {
	mu sync.Mutex
	b  *strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestAnInvalidACPMessageEndsTheTurn_REQ_ACP_006: a line that is not JSON-RPC ends the turn at
// once; the SDK alone would log it and wait for the silence timeout.
func TestAnInvalidACPMessageEndsTheTurn_REQ_ACP_006(t *testing.T) {
	t.Parallel()
	h := newHost(t, nil)
	start := time.Now()
	_, _, err := prompt(t, h, workDir(t), "garbage")
	if !errors.Is(err, domain.ErrAgent) || !strings.Contains(err.Error(), "not ACP") {
		t.Fatalf("garbage ended the turn with %v, want the agent's error", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the turn ended %s after the garbage, want at once", d)
	}
}

// TestASilentACPAgentEndsTheTurn_REQ_ACP_006: an agent that sends nothing for the silence
// timeout, asking nothing of Umbral, ends its turn and is killed.
func TestASilentACPAgentEndsTheTurn_REQ_ACP_006(t *testing.T) {
	t.Parallel()
	h := newHost(t, func(c *Config) { c.SilenceTimeout = 300 * time.Millisecond })
	_, _, err := prompt(t, h, workDir(t), "silent")
	if !errors.Is(err, domain.ErrAgent) || !strings.Contains(err.Error(), "sent nothing") {
		t.Fatalf("a silent agent ended the turn with %v, want the silence named", err)
	}
	if h.Running("thr_1") {
		t.Error("the silent agent was not killed")
	}
}

// slowMediator answers permissions after a delay, longer than the silence timeout.
type slowMediator struct {
	Refuse
	delay time.Duration
}

func (m slowMediator) Permission(ctx context.Context, _ ports.PermissionRequest) (bool, error) {
	select {
	case <-time.After(m.delay):
	case <-ctx.Done():
	}
	return false, nil
}

// TestAnAgentWaitingOnUmbralIsNotSilent_REQ_ACP_006: while Umbral has not answered the agent's
// permission request, the agent is waiting, not silent, however long the answer takes.
func TestAnAgentWaitingOnUmbralIsNotSilent_REQ_ACP_006(t *testing.T) {
	t.Parallel()
	h := newHost(t, func(c *Config) {
		c.SilenceTimeout = 200 * time.Millisecond
		c.Mediator = slowMediator{delay: time.Second}
	})
	s, stop, err := prompt(t, h, workDir(t), "ask")
	if err != nil || stop != "end_turn" {
		t.Fatalf("the turn ended %q, %v while the agent waited on Umbral, want it to go on", stop, err)
	}
	if s.Text0() != "selected reject" {
		t.Errorf("the agent read %q, want its reject_once option", s.Text0())
	}
}

// TestAPendingApprovalPausesTheSilenceTimer_REQ_ACP_006: an approval of the thread pending is
// the user's wait, not the agent's silence. Without it, the same quiet spell ends the turn.
func TestAPendingApprovalPausesTheSilenceTimer_REQ_ACP_006(t *testing.T) {
	t.Parallel()
	var pending atomic.Bool
	pending.Store(true)
	h := newHost(t, func(c *Config) {
		c.SilenceTimeout = 200 * time.Millisecond
		c.Pending = func(thread string) bool { return thread == "thr_1" && pending.Load() }
	})
	dir := workDir(t)
	s, stop, err := prompt(t, h, dir, "slow 800 1")
	if err != nil || stop != "end_turn" || s.Text0() != "0 " {
		t.Fatalf("with an approval pending the turn ended %q, %v (%q), want it to go on", stop, err, s.Text0())
	}

	pending.Store(false)
	if _, _, err := prompt(t, h, dir, "slow 800 1"); !errors.Is(err, domain.ErrAgent) {
		t.Fatalf("with no approval pending the quiet spell ended the turn with %v, want the agent's error", err)
	}
}

// TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009: an entry whose manifest Umbral does not
// have, or a name that is no entry at all, is refused before anything runs.
func TestAnAgentWithoutAManifestIsRefused_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	exe, _ := os.Executable()
	h := newHost(t, func(c *Config) {
		c.Entries = append(c.Entries, domain.Entry{Name: "mystery", Command: exe, Args: acptest.Args()})
	})
	dir := workDir(t)
	for agent, reason := range map[string]string{"mystery": domain.ReasonNoManifest, "absent": domain.ReasonUnknownAgent} {
		err := h.Check(agent, dir)
		var refusal *domain.Refusal
		if !errors.As(err, &refusal) || refusal.Reason != reason {
			t.Errorf("Check(%q) = %v, want a %s refusal", agent, err, reason)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		if err := h.Open(ctx, Request{ThreadID: "thr_" + agent, Agent: agent, Dir: dir}); !errors.As(err, &refusal) {
			t.Errorf("Open(%q) = %v, want it refused", agent, err)
		}
		cancel()
		if h.Running("thr_" + agent) {
			t.Errorf("agent %q was launched", agent)
		}
	}
}

// TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009: of the daemon's environment the agent gets
// PATH and LANG only — not a provider key, not the user's HOME, not UMBRAL_* — plus the
// isolation, the configuration's location and its entry's resolved references.
func TestTheACPEnvironmentHoldsNoSecrets_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	h := newHost(t, func(c *Config) {
		c.Environ = func() []string {
			return []string{
				"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "HOME=/home/someone", "OPENAI_API_KEY=sk-daemon-secret",
				"UMBRAL_PANE_ID=w1:p1", "SSH_AUTH_SOCK=/tmp/agent.sock", "XDG_CONFIG_HOME=/home/someone/.config",
			}
		}
		c.Entries[0].EnvRefs = map[string]string{"MODEL_TOKEN": "keyring:umbral/acp/fake"}
		c.Secrets = secrets{"keyring:umbral/acp/fake": "token-from-keyring"}
	})
	r := report(t, h, workDir(t))
	got := map[string]string{}
	for _, kv := range r.Env {
		name, value, _ := strings.Cut(kv, "=")
		got[name] = value
	}
	want := []string{"PATH", "LANG", "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "OPENCODE_CONFIG", "MODEL_TOKEN"}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("the agent's environment lacks %s", name)
		}
		delete(got, name)
	}
	// What a process adds to its own environment on start, not something inherited.
	delete(got, "PWD")
	if len(got) != 0 {
		t.Errorf("the agent's environment holds more than the allowlist: %v", got)
	}
	for _, kv := range r.Env {
		if strings.Contains(kv, "sk-daemon-secret") || strings.Contains(kv, "/home/someone") {
			t.Errorf("the agent inherited %q", kv)
		}
	}
	if r.Env == nil || !strings.Contains(strings.Join(r.Env, "\n"), "MODEL_TOKEN=token-from-keyring") {
		t.Error("the entry's env reference did not reach the agent resolved")
	}
}

// TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009: the configuration the agent reads
// sets the wildcard and every named permission to `ask`, the wildcard first, declares no MCP
// server and no plugin, and keeps the entry's model. An entry may not set a permission itself.
func TestTheOpenCodeManifestAsksForEveryPermission_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	h := newHost(t, func(c *Config) {
		c.Entries[0].Config = map[string]any{"model": "ollama/gpt-oss:20b"}
	})
	r := report(t, h, workDir(t))
	var cfg struct {
		Permission json.RawMessage `json:"permission"`
		MCP        map[string]any  `json:"mcp"`
		Plugin     []any           `json:"plugin"`
		Model      string          `json:"model"`
	}
	if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
		t.Fatalf("the agent's config %q: %v", r.Config, err)
	}
	var perms map[string]string
	if err := json.Unmarshal(cfg.Permission, &perms); err != nil {
		t.Fatalf("permission %s: %v", cfg.Permission, err)
	}
	for _, name := range append([]string{"*"}, "edit", "bash", "webfetch", "websearch", "external_directory", "task") {
		if perms[name] != "ask" {
			t.Errorf("permission %q is %q, want ask", name, perms[name])
		}
	}
	for name, v := range perms {
		if v != "ask" {
			t.Errorf("permission %q is %q", name, v)
		}
	}
	if !strings.HasPrefix(strings.Join(strings.Fields(string(cfg.Permission)), ""), `{"*":"ask"`) {
		t.Errorf("the wildcard is not the first permission, so a later rule would win over it: %s", cfg.Permission)
	}
	if len(cfg.MCP) != 0 || len(cfg.Plugin) != 0 {
		t.Errorf("the config declares MCP servers %v or plugins %v", cfg.MCP, cfg.Plugin)
	}
	if cfg.Model != "ollama/gpt-oss:20b" {
		t.Errorf("the entry's model is %q in the agent's config", cfg.Model)
	}

	bad := newHost(t, func(c *Config) {
		c.Entries[0].Config = map[string]any{"permission": map[string]any{"bash": "allow"}}
	})
	var refusal *domain.Refusal
	if err := bad.Check("fake", workDir(t)); !errors.As(err, &refusal) || refusal.Reason != domain.ReasonConfigKey {
		t.Errorf("an entry setting a permission: %v, want a config_key refusal", err)
	}
}

// TestARepositoryAgentConfigIsRefused_REQ_ACP_009: OpenCode reads a repository's configuration
// after Umbral's, so a repository holding one — in the directory or above it, up to its top —
// is refused, and so is a relaunch after the agent wrote one itself.
func TestARepositoryAgentConfigIsRefused_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	h := newHost(t, nil)
	for _, name := range []string{"opencode.json", "opencode.jsonc", ".opencode"} {
		top := workDir(t)
		sub := filepath.Join(top, "pkg")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(top, name), []byte(`{"permission":{"bash":"allow"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var refusal *domain.Refusal
		if err := h.Check("fake", sub); !errors.As(err, &refusal) || refusal.Reason != domain.ReasonProjectConfig {
			t.Errorf("%s above the directory: %v, want a project_config refusal", name, err)
		}
	}

	// A `.git` git would refuse is not a repository's top: the search goes on past it, as the
	// agent's own would; a valid gitfile stops it.
	top := workDir(t)
	if err := os.WriteFile(filepath.Join(top, "opencode.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"empty": "", "bogus": "gitdir: /nonexistent\n"} {
		sub := filepath.Join(top, name)
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, ".git"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		var refusal *domain.Refusal
		if err := h.Check("fake", sub); !errors.As(err, &refusal) || refusal.Reason != domain.ReasonProjectConfig {
			t.Errorf("an %s .git between the directory and opencode.json: %v, want a project_config refusal", name, err)
		}
	}
	linked := filepath.Join(top, "linked")
	gitDir(t, filepath.Join(top, "real-git"))
	if err := os.Mkdir(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: ../real-git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Check("fake", linked); err != nil {
		t.Errorf("a gitfile naming a repository's gitdir is its top: %v, want no refusal", err)
	}
	// What `git worktree add` writes: a gitdir with HEAD, refs and a commondir, and no objects of
	// its own; and a `.git` that is a symlink to a gitdir, which git follows.
	wt := filepath.Join(top, "wt")
	wtGit := filepath.Join(top, "real-git", "worktrees", "wt")
	if err := os.MkdirAll(filepath.Join(wtGit, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"HEAD": "ref: refs/heads/wt\n", "commondir": "../..\n"} {
		if err := os.WriteFile(filepath.Join(wtGit, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGit+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Check("fake", wt); err != nil {
		t.Errorf("a linked worktree's gitfile is its top: %v, want no refusal", err)
	}
	symlinked := filepath.Join(top, "symlinked")
	if err := os.Mkdir(symlinked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(top, "real-git"), filepath.Join(symlinked, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := h.Check("fake", symlinked); err != nil {
		t.Errorf("a .git symlinked to a gitdir is its top: %v, want no refusal", err)
	}

	dir := workDir(t)
	if _, stop, err := prompt(t, h, dir, "crash"); err == nil {
		t.Fatalf("crash: %q", stop)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var refusal *domain.Refusal
	if _, _, err := prompt(t, h, dir, "again"); !errors.As(err, &refusal) || refusal.Reason != domain.ReasonProjectConfig {
		t.Errorf("a relaunch over a repository configuration: %v, want it refused", err)
	}
}

// TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009: the agent's HOME and XDG directories are
// its own, under the host's directory, so its user config directory holds only what Umbral
// wrote; and they are removed with it.
func TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "acp")
	h := newHost(t, func(c *Config) { c.Dir = root })
	r := report(t, h, workDir(t))
	env := map[string]string{}
	for _, kv := range r.Env {
		name, value, _ := strings.Cut(kv, "=")
		env[name] = value
	}
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if !strings.HasPrefix(env[name], filepath.Join(root, "thr_1")+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a directory of the thread's own under %s", name, env[name], root)
		}
	}
	if len(r.GlobalConfig) != 1 || r.GlobalConfig[0] != "opencode" {
		t.Errorf("the agent's config directory holds %v, want only Umbral's opencode/", r.GlobalConfig)
	}
	h.Close("thr_1")
	if _, err := os.Stat(filepath.Join(root, "thr_1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the agent's home outlived it: %v", err)
	}
}

// TestAnACPAgentIsLaunchedInItsDirectory_REQ_ACP_001: the process runs in the directory it was
// given, its session is opened there, and initialize advertised files and terminals.
func TestAnACPAgentIsLaunchedInItsDirectory_REQ_ACP_001(t *testing.T) {
	t.Parallel()
	h := newHost(t, nil)
	dir := workDir(t)
	r := report(t, h, dir)
	if r.Cwd != dir || r.SessionCwd != dir {
		t.Errorf("the agent runs in %q with its session in %q, want %q", r.Cwd, r.SessionCwd, dir)
	}
	if !r.ReadFile || !r.WriteFile || !r.Terminal {
		t.Errorf("initialize advertised fs read %v, write %v, terminal %v; want all", r.ReadFile, r.WriteFile, r.Terminal)
	}
}

// TestAnACPTurnStreamsItsReports_REQ_ACP_002: text, thoughts and a tool call reach the sink,
// the call merged from its start and its update.
func TestAnACPTurnStreamsItsReports_REQ_ACP_002(t *testing.T) {
	t.Parallel()
	h := newHost(t, nil)
	s, stop, err := prompt(t, h, workDir(t), "stream")
	if err != nil || stop != "end_turn" {
		t.Fatalf("stream: %q, %v", stop, err)
	}
	if s.Text0() != "hello world" || len(s.thoughts) != 1 || s.thoughts[0] != "pondering" {
		t.Errorf("text %q, thoughts %v", s.Text0(), s.thoughts)
	}
	if len(s.calls) != 2 {
		t.Fatalf("tool call reports %+v, want its start and its end", s.calls)
	}
	last := s.calls[1]
	if last.ID != "call-1" || last.Kind != "execute" || last.Title != "ls" || last.Status != "completed" ||
		last.Output != "a.txt" || string(last.RawInput) != `{"command":"ls"}` {
		t.Errorf("the ended call is %+v", last)
	}
}

// TestACancelledACPTurnSendsSessionCancel_REQ_ACP_008: a cancel is `session/cancel`; the
// agent ends its turn `cancelled` and keeps running for the next one.
func TestACancelledACPTurnSendsSessionCancel_REQ_ACP_008(t *testing.T) {
	t.Parallel()
	h := newHost(t, nil)
	dir := workDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	stop, err := h.Prompt(ctx, Request{ThreadID: "thr_1", Agent: "fake", Dir: dir}, "silent", &sink{})
	if stop != "cancelled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled turn ended %q, %v", stop, err)
	}
	if !h.Running("thr_1") {
		t.Error("an agent that ended its cancelled turn was killed")
	}
}
