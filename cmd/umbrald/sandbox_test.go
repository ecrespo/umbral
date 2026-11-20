package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/acp/adapters/sdk/acptest"
	acpdomain "github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/sandbox/adapters/landlock"
	sandboxdomain "github.com/ecrespo/umbral/internal/sandbox/domain"
)

// needSandbox skips where this system cannot confine: the kernel's refusals are what these
// tests are about.
func needSandbox(t *testing.T) {
	t.Helper()
	if st := landlock.New("x").Status(); !st.Enforcing() {
		// Where the sandbox is required (CI's Linux job) a system that cannot confine is a
		// failure: a skip would leave the kernel's refusals unproven behind a green run.
		if os.Getenv("UMBRAL_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("UMBRAL_REQUIRE_SANDBOX is set and this system cannot confine: %+v", st)
		}
		t.Skipf("no sandbox on this system: %+v", st)
	}
}

// hostedThread creates a thread hosted by the fake agent in a worktree of repo, and returns
// it with the worktree's path.
func hostedThread(t *testing.T, ctx context.Context, stream *client.Stream, repo string) (acpThread, string) {
	t.Helper()
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo, "worktree": true, "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	var wt struct{ Path string }
	if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": thread.WorktreeID}, &wt); err != nil {
		t.Fatal(err)
	}
	return thread, wt.Path
}

// agentDoes has the fake agent do one thing itself, without asking Umbral, and returns what
// it answered.
func agentDoes(t *testing.T, ctx context.Context, stream *client.Stream, threadID, line string) string {
	t.Helper()
	sendAndFinish(t, ctx, stream, threadID, line)
	return lastAnswer(t, ctx, stream, threadID)
}

// TestAHostedAgentIsConfinedByTheKernel_REQ_SBX_001: a hosted agent that writes without asking
// is refused by the kernel outside its thread's worktree and its own home — the repository's
// main copy and the user's files included — and writes inside them. `system.status` says the
// sandbox enforces and the thread says it is confined.
func TestAHostedAgentIsConfinedByTheKernel_REQ_SBX_001(t *testing.T) {
	needSandbox(t)
	repo, env := gitRepo(t)
	stream, ctx := acpDaemon(t, env...)
	thread, worktree := hostedThread(t, ctx, stream, repo)
	if thread.Sandbox == nil || *thread.Sandbox != "confined" {
		t.Errorf("thread.create's sandbox = %v, want confined", thread.Sandbox)
	}
	var status api.StatusResult
	if err := stream.Call(ctx, "system.status", map[string]any{}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Sandbox.State != api.SandboxEnforcing || status.Sandbox.Mechanism != landlock.Mechanism || status.Sandbox.Reason != "" {
		t.Errorf("system.status sandbox = %+v, want enforcing by landlock", status.Sandbox)
	}

	outside := filepath.Join(t.TempDir(), "planted")
	for name, target := range map[string]string{
		"the main copy":        filepath.Join(repo, "planted"),
		"the main copy's hook": filepath.Join(repo, ".git", "hooks", "pre-commit"),
		"a directory outside":  outside,
	} {
		if got := agentDoes(t, ctx, stream, thread.ID, "touch "+target); !strings.Contains(got, "permission denied") {
			t.Errorf("%s: the agent's own write answered %q, want the kernel's refusal", name, got)
		}
		if _, err := os.Stat(target); err == nil {
			t.Errorf("%s was written", name)
		}
	}
	inside := filepath.Join(worktree, "made.txt")
	if got := agentDoes(t, ctx, stream, thread.ID, "touch "+inside); got != "written" {
		t.Errorf("a write in the thread's worktree answered %q", got)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("the file in the worktree is not there: %v", err)
	}
	r := agentReport(t, ctx, stream, thread.ID)
	for _, kv := range r.Env {
		if tmp, ok := strings.CutPrefix(kv, "TMPDIR="); ok {
			if got := agentDoes(t, ctx, stream, thread.ID, "touch "+filepath.Join(tmp, "scratch")); got != "written" {
				t.Errorf("a write in the agent's temporary directory answered %q", got)
			}
		}
	}
}

// TestAHostedAgentCannotReadUmbralsData_REQ_SBX_002: the kernel refuses a hosted agent the
// database, the token and another thread's worktree; the repository's main copy stays readable.
func TestAHostedAgentCannotReadUmbralsData_REQ_SBX_002(t *testing.T) {
	needSandbox(t)
	repo, env := gitRepo(t)
	stream, ctx := acpDaemon(t, env...)
	thread, _ := hostedThread(t, ctx, stream, repo)
	_, sibling := hostedThread(t, ctx, stream, repo)
	if err := os.WriteFile(filepath.Join(sibling, "sibling.txt"), []byte("another thread's work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("the main copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")
	token := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "umbral", config.TokenFileName)
	for _, path := range []string{db, token} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the test looks for %s, which is not there: %v", path, err)
		}
	}
	for name, path := range map[string]string{
		"the database":              db,
		"the token":                 token,
		"another thread's worktree": filepath.Join(sibling, "sibling.txt"),
	} {
		if got := agentDoes(t, ctx, stream, thread.ID, "cat "+path); !strings.Contains(got, "permission denied") {
			t.Errorf("%s: the agent's own read answered %q, want the kernel's refusal", name, got)
		}
	}
	if got := agentDoes(t, ctx, stream, thread.ID, "cat "+filepath.Join(repo, "main.txt")); got != "the main copy" {
		t.Errorf("the main copy answered %q", got)
	}
}

// TestTheSandboxOptOutIsSaid_REQ_SBX_003: with `[sandbox] mode = "off"` a hosted agent runs
// unconfined, and both `system.status` and the thread say so.
func TestTheSandboxOptOutIsSaid_REQ_SBX_003(t *testing.T) {
	repo, env := gitRepo(t)
	stream, ctx := acpDaemonSettings(t, []string{acptest.Arg}, "\n[sandbox]\nmode = \"off\"\n", env...)
	thread, _ := hostedThread(t, ctx, stream, repo)
	if thread.Sandbox == nil || *thread.Sandbox != "unconfined" {
		t.Errorf("thread.create's sandbox = %v, want unconfined", thread.Sandbox)
	}
	var status api.StatusResult
	if err := stream.Call(ctx, "system.status", map[string]any{}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Sandbox.State != api.SandboxOff || status.Sandbox.Reason != sandboxdomain.ReasonConfig {
		t.Errorf("system.status sandbox = %+v, want off by config", status.Sandbox)
	}
	outside := filepath.Join(t.TempDir(), "written-unconfined")
	if got := agentDoes(t, ctx, stream, thread.ID, "touch "+outside); got != "written" {
		t.Errorf("an unconfined agent's write answered %q", got)
	}

	// Umbral's own agent is not covered: its thread carries no sandbox state.
	var own acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo}, &own); err != nil {
		t.Fatal(err)
	}
	if own.Sandbox != nil {
		t.Errorf("Umbral's own thread says sandbox %q", *own.Sandbox)
	}
}

// TestAHostedAgentWritesNothingInTheRepositorysGitDirectory_REQ_SBX_001: the agent can write
// its worktree's `.git` file, and no launch reads it: rewritten to name the directory holding
// every sibling worktree, or left as git wrote it, it opens no path. After the agent is
// launched again, a sibling's file is unreadable and unwritable, and so are the repository's
// objects, its refs and the worktree's own record there.
func TestAHostedAgentWritesNothingInTheRepositorysGitDirectory_REQ_SBX_001(t *testing.T) {
	needSandbox(t)
	repo, env := gitRepo(t)
	stream, ctx := acpDaemon(t, env...)
	thread, worktree := hostedThread(t, ctx, stream, repo)
	_, sibling := hostedThread(t, ctx, stream, repo)
	secret := filepath.Join(sibling, "sibling.txt")
	if err := os.WriteFile(secret, []byte("another thread's work"), 0o600); err != nil {
		t.Fatal(err)
	}
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(worktree))
	inGit := map[string]string{
		"an object":              filepath.Join(repo, ".git", "objects", "planted"),
		"a ref":                  filepath.Join(repo, ".git", "refs", "heads", "planted"),
		"the record's commondir": filepath.Join(admin, "commondir"),
		"a worktree config":      filepath.Join(admin, "config.worktree"),
	}
	for name, target := range inGit {
		if got := agentDoes(t, ctx, stream, thread.ID, "put "+target+" planted"); !strings.Contains(got, "permission denied") {
			t.Errorf("%s: the agent's write answered %q, want the kernel's refusal", name, got)
		}
	}
	// `<data>/worktrees/<repository>`, whose base passes for a repository's `worktrees/<name>`.
	target := filepath.Dir(worktree)
	if got := agentDoes(t, ctx, stream, thread.ID, "put "+filepath.Join(worktree, ".git")+" gitdir: "+target); got != "written" {
		t.Fatalf("the agent could not rewrite its own .git: %q", got)
	}
	sendAndFinish(t, ctx, stream, thread.ID, "crash")
	if got := agentDoes(t, ctx, stream, thread.ID, "cat "+secret); !strings.Contains(got, "permission denied") {
		t.Errorf("after the relaunch the sibling's file answered %q, want the kernel's refusal", got)
	}
	if got := agentDoes(t, ctx, stream, thread.ID, "touch "+filepath.Join(sibling, "planted")); !strings.Contains(got, "permission denied") {
		t.Errorf("after the relaunch a write in the sibling's worktree answered %q", got)
	}
	for name, target := range inGit {
		if got := agentDoes(t, ctx, stream, thread.ID, "put "+target+" planted"); !strings.Contains(got, "permission denied") {
			t.Errorf("after the relaunch, %s: the agent's write answered %q", name, got)
		}
	}
}

// TestAThreadWhoseDirectoryHoldsUmbralsDataIsNotLaunched_REQ_SBX_002: a rule on a directory
// covers everything beneath it, so an agent working in a directory that holds Umbral's data
// would read and write the database. It is not launched: the thread is kept and says why.
func TestAThreadWhoseDirectoryHoldsUmbralsDataIsNotLaunched_REQ_SBX_002(t *testing.T) {
	needSandbox(t)
	stream, ctx := acpDaemon(t)
	holds := os.Getenv("XDG_DATA_HOME") // the parent of the daemon's data directory
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": holds, "agent": "fake"}, &thread); err != nil {
		t.Fatalf("thread.create: %v, want the thread kept", err)
	}
	if thread.AgentUnavailable == nil || !strings.Contains(thread.AgentUnavailable.Message, "cannot be confined") {
		t.Fatalf("thread.create = %+v, want the agent not launched because it cannot be confined", thread)
	}
}

// TestAnAgentIsVouchedOnlyItsOwnDirectories_REQ_SBX_002: the confiner gives a thread the one
// directory inside Umbral's data that is its own worktree, and the isolation root the daemon
// makes for it; another thread's worktree, the worktrees' parent, a root elsewhere and a
// daemon binary the agent could replace are all refused. With no sandbox it returns the
// command as it came, which is what a system that cannot confine does (REQ-SBX-003).
func TestAnAgentIsVouchedOnlyItsOwnDirectories_REQ_SBX_002(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, run := filepath.Join(base, "data"), filepath.Join(base, "run")
	mine := filepath.Join(data, "worktrees", "repo1", "thr_1")
	for _, d := range []string{mine, filepath.Join(data, "worktrees", "repo1", "thr_2"), filepath.Join(run, "acp", "thr_1"), filepath.Join(base, "src")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c := acpConfiner{
		sandbox: stubSandbox{state: sandboxdomain.StateEnforcing}, protected: []string{data, run},
		helper: "/usr/bin/umbrald", worktrees: filepath.Join(data, "worktrees"), homes: filepath.Join(run, "acp"),
	}
	root := filepath.Join(run, "acp", "thr_1")
	ask := func(dir, root string) error {
		_, err := c.Confine(acpdomainConfinement("thr_1", dir, root), []string{"agent"})
		return err
	}
	if err := ask(mine, root); err != nil {
		t.Errorf("the thread's own worktree: %v", err)
	}
	if err := ask(filepath.Join(base, "src"), root); err != nil {
		t.Errorf("a directory outside Umbral's: %v", err)
	}
	for name, dir := range map[string]string{
		"another thread's worktree":  filepath.Join(data, "worktrees", "repo1", "thr_2"),
		"the repository's worktrees": filepath.Join(data, "worktrees", "repo1"),
		"every worktree":             filepath.Join(data, "worktrees"),
		"the data directory":         data,
		"a directory holding it":     base,
	} {
		if err := ask(dir, root); !errors.Is(err, sandboxdomain.ErrProfile) {
			t.Errorf("%s: %v, want ErrProfile", name, err)
		}
	}
	if err := ask(mine, filepath.Join(base, "src")); !errors.Is(err, sandboxdomain.ErrProfile) {
		t.Errorf("an isolation root that is not the daemon's: %v, want ErrProfile", err)
	}
	c.helper = filepath.Join(base, "src", "bin", "umbrald")
	if err := ask(filepath.Join(base, "src"), root); !errors.Is(err, sandboxdomain.ErrProfile) {
		t.Errorf("a daemon binary inside the thread's directory: %v, want ErrProfile", err)
	}

	c.sandbox = stubSandbox{state: sandboxdomain.StateUnavailable, reason: sandboxdomain.ReasonNoLandlock}
	argv, err := c.Confine(acpdomainConfinement("thr_1", base, "/anywhere"), []string{"agent", "acp"})
	if err != nil || len(argv) != 2 || argv[0] != "agent" {
		t.Errorf("with no sandbox: %v, %v; want the command as it came", argv, err)
	}
	if st := sandboxStatus(c.sandbox); st.State != api.SandboxUnavailable || st.Reason != sandboxdomain.ReasonNoLandlock || st.Mechanism != "" {
		t.Errorf("system.status of a kernel without Landlock = %+v", st)
	}
}

// stubSandbox is a sandbox in a given state that confines nothing: the profile is what these
// tests are about.
type stubSandbox struct {
	state  sandboxdomain.State
	reason string
}

func (s stubSandbox) Status() sandboxdomain.Status {
	return sandboxdomain.Status{State: s.state, Reason: s.reason}
}

func (stubSandbox) Command(_ sandboxdomain.Profile, argv []string) ([]string, error) {
	return argv, nil
}

func acpdomainConfinement(thread, dir, root string) acpdomain.Confinement {
	return acpdomain.Confinement{ThreadID: thread, Dir: dir, Root: root}
}

// TestASandboxWriteEntryThatReachesUmbralStopsTheDaemon_REQ_SBX_004: `[sandbox] write` may not
// name, hold or lie inside the data directory, the runtime directory or the cache the daemon
// loads a library from; build caches are accepted.
func TestASandboxWriteEntryThatReachesUmbralStopsTheDaemon_REQ_SBX_004(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	data, run := "/home/u/.local/share/umbral", "/run/user/1000/umbral"
	if _, _, err := newSandbox(config.Sandbox{Write: []string{"/home/u/.cache/go-build"}}, data, run, logger); err != nil {
		t.Fatalf("a build cache was refused: %v", err)
	}
	entries := map[string]string{
		"the data directory":    data,
		"a worktree inside it":  data + "/worktrees",
		"the runtime directory": run,
		"a parent of the data":  "/home/u/.local/share",
	}
	if cache := codeCacheDir(); cache != "" {
		entries["the code cache"] = cache
		entries["the code cache's parent"] = filepath.Dir(cache)
	} else if runtime.GOOS != "windows" {
		t.Error("no code cache directory to protect")
	}
	for name, entry := range entries {
		if _, _, err := newSandbox(config.Sandbox{Write: []string{entry}}, data, run, logger); err == nil {
			t.Errorf("%s (%s) was accepted", name, entry)
		}
	}
	// A link to the data directory is the data directory.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newSandbox(config.Sandbox{Write: []string{link}}, real, run, logger); err == nil {
		t.Error("a write entry that is a link to the data directory was accepted")
	}
	// The opt-out does not excuse the list: it is checked whatever the mode.
	if _, _, err := newSandbox(config.Sandbox{Off: true, Write: []string{data}}, data, run, logger); err == nil {
		t.Error("with the sandbox off, a write entry naming the data directory was accepted")
	}
}
