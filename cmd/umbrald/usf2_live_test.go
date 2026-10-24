//go:build live

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// US-F2-001, the F2a exit criterion (PRD F2 §9, T-F2-12), against a real daemon, a real
// gpt-oss:20b on the local Ollama and a real OpenCode over ACP: two threads of Umbral's own
// and one hosted thread, each in its own worktree of one repository, running at once. What is
// asserted is what the daemon did — the worktrees, the main copy, the merges, the rows and the
// traces — and, logged rather than asserted, whether each agent did its task, since a model's
// work is not deterministic. UMBRAL_OPENCODE names the OpenCode binary; without one the test
// skips.

// collectedSpan is what the scenario reads of an exported span.
type collectedSpan struct {
	name, traceID, spanID, parentID, turnID string
}

// collected reads the spans the collector holds.
func (c *otlpCollector) collected() []collectedSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]collectedSpan, 0, len(c.spans))
	for _, s := range c.spans {
		out = append(out, collectedSpan{
			name: s.GetName(), traceID: fmt.Sprintf("%x", s.GetTraceId()), spanID: fmt.Sprintf("%x", s.GetSpanId()),
			parentID: fmt.Sprintf("%x", s.GetParentSpanId()), turnID: spanAttrs(s)["umbral.turn.id"],
		})
	}
	return out
}

// opencodeBinary is the OpenCode the hosted thread runs, or a skip.
func opencodeBinary(t *testing.T) string {
	t.Helper()
	for _, p := range []string{os.Getenv("UMBRAL_OPENCODE"), filepath.Join(os.Getenv("HOME"), ".opencode", "bin", "opencode")} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("opencode"); err == nil {
		return p
	}
	t.Skip("OpenCode is not installed; set UMBRAL_OPENCODE")
	return ""
}

// usf2Repo makes the scenario's repository with e2e/usf2/fixture.sh.
func usf2Repo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "stats")
	fixture, err := filepath.Abs("../../e2e/usf2/fixture.sh")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), "bash", fixture, repo).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	return repo
}

// git runs git in dir and returns its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// usf2Thread is what the scenario reads of a thread.
type usf2Thread struct {
	ID         string  `json:"id"`
	Agent      *string `json:"agent"`
	Egress     string  `json:"egress_visibility"`
	WorktreeID string  `json:"worktree_id"`
	Refused    *struct {
		Reason, Message string
	} `json:"worktree_refused"`
	Unavailable *struct {
		Reason, Message string
	} `json:"agent_unavailable"`
}

// usf2Task is one of the scenario's three threads.
type usf2Task struct {
	name, agent, prompt string
	// files are the paths the task may touch.
	files []string
}

var usf2Tasks = []usf2Task{
	{
		name: "fix", prompt: "Run `go test ./...` in this directory. One test fails because of a bug in stats.go. " +
			"Fix the bug in stats.go so the test passes, run `go test ./...` again to check, and change no other file.",
		files: []string{"stats.go"},
	},
	{
		name: "readme", prompt: "README.md is out of date. Read the .go files in this directory and rewrite the " +
			"\"Functions\" list in README.md so it names exactly the exported functions they define, one line each. " +
			"Change only README.md.",
		files: []string{"README.md"},
	},
	{
		name: "rename", agent: "opencode", prompt: "Rename the Go function Mean to Average everywhere in this " +
			"repository: its definition, its doc comment and every caller. Then run `go build ./...` to check. " +
			"Change only .go files.",
		files: []string{"stats.go", "report.go", "stats_test.go"},
	},
}

// TestLiveUSF2001_REQ_ORC_001 runs US-F2-001 (PRD F2 §9) and checks the F2a criteria of PRD F2
// §4 that a run can measure: the three threads finish; nothing changes outside their
// worktrees until a merge; each worktree's diff stays within its task's files; the merges land
// or report their conflict; every turn has a trace; and every write, command and fetch of the
// hosted thread was decided by Umbral's policy engine.
func TestLiveUSF2001_REQ_ORC_001(t *testing.T) {
	url := os.Getenv("UMBRAL_OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	model := os.Getenv("UMBRAL_LIVE_MODEL")
	if model == "" {
		model = "gpt-oss:20b"
	}
	requireModel(t, url, model)
	opencode := opencodeBinary(t)
	requireIdleModel(t, url, model)
	repo := usf2Repo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	otlp := &otlpCollector{}
	collector := otlp.server(t).URL

	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "models.toml"), fmt.Sprintf(`
[router]
offline = true

[classes]
code = ["ollama/%[1]s"]
fast = ["ollama/%[1]s"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = %[2]q
[providers.options]
num_ctx = 16384
keep_alive = "30m"
`, model, url))
	writeFile(t, filepath.Join(configDir, "config.toml"), fmt.Sprintf(`
[otel]
endpoint = %[1]q

[agents]
max_concurrent_turns = 3

[acp]
silence_timeout = 900

[[acp.agents]]
name = "opencode"
manifest = "opencode"
command = %[2]q
args = ["acp"]
[acp.agents.config]
model = "ollama/%[3]s"
[acp.agents.config.provider.ollama]
npm = "@ai-sdk/openai-compatible"
name = "Ollama"
[acp.agents.config.provider.ollama.options]
baseURL = "%[4]s/v1"
[acp.agents.config.provider.ollama.models.%[5]q]
name = %[3]q
`, collector, opencode, model, url, model))
	// The simulated user approves every command, so the panes' shells run in a home of their
	// own and nothing may fetch a Go toolchain or a module, as in US-003.
	goEnv := offlineGoEnv(t)
	stop := startStoppableDaemon(t, bin, rt, goEnv...)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Minute)
	defer cancel()
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := client.NewStream(c)
	defer func() { _ = stream.Close() }()
	waitHealthy(t, ctx, stream, "ollama/"+model)

	var ws struct {
		Workspace struct{ ID string } `json:"workspace"`
	}
	if err := stream.Call(ctx, "workspace.create", map[string]any{"cwd": repo, "label": "us-f2-001"}, &ws); err != nil {
		t.Fatal(err)
	}

	// The three threads, each with its worktree.
	threads := map[string]usf2Thread{}
	byID := map[string]usf2Task{}
	for _, task := range usf2Tasks {
		params := map[string]any{"cwd": repo, "title": task.name, "workspace": ws.Workspace.ID, "worktree": true}
		if task.agent != "" {
			params["agent"] = task.agent
		}
		var th usf2Thread
		if err := stream.Call(ctx, "thread.create", params, &th); err != nil {
			t.Fatalf("create %s: %v", task.name, err)
		}
		if th.WorktreeID == "" || th.Refused != nil || th.Unavailable != nil {
			t.Fatalf("thread %s: %+v", task.name, th)
		}
		if task.agent != "" && th.Egress != "unobserved" {
			t.Errorf("the hosted thread is %q, want unobserved", th.Egress)
		}
		threads[task.name], byID[th.ID] = th, task
		t.Logf("thread %s: %s, worktree %s", task.name, th.ID, th.WorktreeID)
	}

	// All three at once.
	started := time.Now()
	for _, task := range usf2Tasks {
		if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": threads[task.name].ID, "text": task.prompt}, nil); err != nil {
			t.Fatalf("send %s: %v", task.name, err)
		}
	}
	turns := map[string]string{} // turn id → thread id
	stops := map[string]string{}
	took := map[string]time.Duration{}
	approvals := map[string][]string{}
	for len(stops) < len(usf2Tasks) {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "approval.requested":
				var a struct {
					ID       string `json:"id"`
					ThreadID string `json:"thread_id"`
					Tool     string `json:"tool"`
					Risk     string `json:"risk"`
					Summary  string `json:"summary"`
				}
				_ = n.Decode(&a)
				// The user of the scenario approves the work and nothing that leaves the machine.
				decision := "approve"
				if a.Risk == "Network" {
					decision = "deny"
				}
				approvals[a.ThreadID] = append(approvals[a.ThreadID], fmt.Sprintf("%s %s %s → %s", a.Tool, a.Risk, oneLineSummary(a.Summary), decision))
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": decision, "scope": "once"}, nil); err != nil {
					t.Logf("approval %s: %v", a.ID, err)
				}
			case "thread.turn_finished":
				var end struct {
					ThreadID   string `json:"thread_id"`
					TurnID     string `json:"turn_id"`
					StopReason string `json:"stop_reason"`
				}
				_ = n.Decode(&end)
				if _, ours := byID[end.ThreadID]; !ours {
					continue
				}
				turns[end.TurnID] = end.ThreadID
				stops[end.ThreadID] = end.StopReason
				took[end.ThreadID] = time.Since(started)
				t.Logf("%s finished %s after %s", byID[end.ThreadID].name, end.StopReason, took[end.ThreadID].Round(time.Second))
			}
		case <-ctx.Done():
			t.Fatalf("not every thread finished: %v", stops)
		}
	}

	// 3/3 finished.
	for id, reason := range stops {
		if reason != "end_turn" {
			t.Errorf("%s ended %s, want end_turn", byID[id].name, reason)
		}
	}
	for name, th := range threads {
		t.Logf("%s approvals: %v", name, approvals[th.ID])
	}

	// The main copy is untouched until a merge.
	if got := git(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("the main copy moved to %s before any merge", got)
	}
	if st := git(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the main copy changed before any merge:\n%s", st)
	}

	// Each worktree's diff stays within its task's files; each task's own check is logged.
	worktrees := map[string]string{}
	var hostedChanges []string
	for _, task := range usf2Tasks {
		th := threads[task.name]
		var wt struct {
			Path string `json:"path"`
		}
		if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": th.WorktreeID}, &wt); err != nil {
			t.Fatal(err)
		}
		worktrees[task.name] = wt.Path
		var diff struct {
			Revision string `json:"revision"`
			Files    []struct{ Path, Status string }
		}
		if err := stream.Call(ctx, "worktree.diff", map[string]any{"worktree_id": th.WorktreeID}, &diff); err != nil {
			t.Fatal(err)
		}
		paths := make([]string, 0, len(diff.Files))
		for _, f := range diff.Files {
			paths = append(paths, f.Path+":"+f.Status)
			if !slices.Contains(task.files, f.Path) {
				t.Errorf("%s changed %s, outside its task's files %v", task.name, f.Path, task.files)
			}
		}
		t.Logf("%s diff: %v", task.name, paths)
		if task.agent != "" {
			for _, f := range diff.Files {
				hostedChanges = append(hostedChanges, filepath.Join(wt.Path, f.Path))
			}
		}
		t.Logf("%s check: %s", task.name, usf2Check(t, task.name, wt.Path))
	}

	// Every write, command and fetch of the hosted thread was decided by the policy engine.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT tool, risk, decision, status, args_json FROM tool_calls WHERE thread_id = ? ORDER BY started_at`, threads["rename"].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var calls, sideEffects int
	written := map[string]bool{} // paths of the allowed fs/write_text_file requests
	for rows.Next() {
		var tool, risk, status, args string
		var decision sql.NullString
		if err := rows.Scan(&tool, &risk, &decision, &status, &args); err != nil {
			t.Fatal(err)
		}
		calls++
		t.Logf("acp row: %s %s %s %s %.160s", tool, risk, decision.String, status, args)
		if tool == "acp:fs/write_text_file" && decision.String == "allow" && status == "ok" {
			var a struct{ Path string }
			_ = json.Unmarshal([]byte(args), &a)
			written[a.Path] = true
		}
		if risk == "ReadOnly" {
			continue
		}
		sideEffects++
		switch {
		case slices.Contains([]string{"allow", "deny", "ask"}, decision.String):
		case tool == "acp:other" && unavailableTool(args):
			// The agent's report that the model named a tool it does not have: nothing ran.
			t.Logf("not a side effect: the model called a tool the agent does not have: %s", args)
		default:
			t.Errorf("a %s %s of the hosted thread was %q, not decided by the policy engine: %s", risk, tool, decision.String, args)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("hosted thread: %d tool_calls rows, %d side effects", calls, sideEffects)
	// Art. 4: Umbral's own threads sent nothing off the machine (the hosted agent's own traffic
	// is unobserved, REQ-ACP-007).
	var egress int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM egress_log`).Scan(&egress); err != nil {
		t.Fatal(err)
	}
	t.Logf("egress_log rows: %d", egress)
	if egress != 0 {
		t.Errorf("egress_log holds %d rows after an offline run", egress)
	}
	// Agent-agnostic: every file the hosted thread changed was written by Umbral on an allowed
	// request, so no write happened in the agent's own process.
	for _, path := range hostedChanges {
		if !written[path] {
			t.Errorf("the hosted thread changed %s without an allowed fs/write_text_file (written: %v)", path, written)
		}
	}
	if sideEffects == 0 {
		t.Error("the hosted thread recorded no write or command; the rename cannot have been done through Umbral")
	}

	// The merges, in the scenario's order: each lands or reports its conflict.
	for _, task := range usf2Tasks {
		var merged struct {
			Commit *string `json:"commit"`
		}
		err := stream.Call(ctx, "worktree.merge", map[string]any{"worktree_id": threads[task.name].WorktreeID, "message": "US-F2-001: " + task.name}, &merged)
		var rpcErr *client.Error
		switch {
		case err == nil:
			commit := "nothing to land"
			if merged.Commit != nil {
				commit = *merged.Commit
			}
			t.Logf("merge %s: %s", task.name, commit)
		case errors.As(err, &rpcErr) && rpcErr.DomainCode == "CONFLICT":
			t.Logf("merge %s: conflict reported: %v", task.name, err)
			if st := git(t, repo, "status", "--porcelain"); st != "" {
				t.Errorf("a refused merge of %s left the main copy changed:\n%s", task.name, st)
			}
		default:
			t.Errorf("merge %s: %v", task.name, err)
		}
	}
	t.Logf("main copy after the merges:\n%s", git(t, repo, "log", "--oneline"))
	t.Logf("main copy check: %s", usf2Check(t, "all", repo))

	// Every turn has a trace: stop the daemon, which flushes its exporter, then read them.
	_ = stream.Close()
	stop(os.Interrupt)
	if left := processesUnder(t, rt); len(left) > 0 {
		t.Errorf("processes of the run outlived the daemon: %v", left)
	}
	got := otlp.collected()
	turnSpans := map[string]collectedSpan{}
	for _, s := range got {
		if s.name == "agent.turn" {
			turnSpans[s.turnID] = s
		}
	}
	for turn, thread := range turns {
		ts, ok := turnSpans[turn]
		if !ok {
			t.Errorf("%s's turn %s has no trace", byID[thread].name, turn)
			continue
		}
		var children []string
		for _, s := range got {
			if s.parentID == ts.spanID {
				children = append(children, s.name)
			}
		}
		sort.Strings(children)
		t.Logf("%s's trace %s: %d child spans %v", byID[thread].name, ts.traceID, len(children), compact(children))
		if byID[thread].agent != "" && !slices.ContainsFunc(children, func(n string) bool { return strings.HasPrefix(n, "tool.acp:") }) {
			t.Errorf("the hosted turn's trace has no tool.acp span: %v", children)
		}
	}
	t.Logf("%d spans collected, %d turns, %d with a trace", len(got), len(turns), len(turnSpans))
}

// unavailableTool reports whether an `other` call's arguments are OpenCode's report that the
// model called a tool the agent does not have — its `invalid` tool, which runs nothing.
func unavailableTool(args string) bool {
	var a struct {
		RawInput struct {
			Error string `json:"error"`
			Tool  string `json:"tool"`
		} `json:"raw_input"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return false
	}
	return strings.HasPrefix(a.RawInput.Error, "Model tried to call unavailable tool")
}

// offlineGoEnv is US-003's environment for anything that runs Go during a live run: a home of
// its own, the developer's build and module caches, and no toolchain or module fetched.
func offlineGoEnv(t *testing.T) []string {
	t.Helper()
	env := make([]string, 0, 6)
	env = append(env, "HOME="+t.TempDir(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=mod")
	for _, k := range []string{"GOCACHE", "GOMODCACHE"} {
		out, err := exec.CommandContext(t.Context(), "go", "env", k).Output()
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, k+"="+strings.TrimSpace(string(out)))
	}
	return env
}

// processesUnder lists the processes, other than this one, whose environment names dir: what
// the daemon started — shells, the hosted agent and its children — inherits its runtime
// directory or the agent's home beneath it.
func processesUnder(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Logf("no /proc, processes not checked: %v", err)
		return nil
	}
	left := make([]string, 0, 4)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil || !strings.Contains(string(env), dir) {
			continue
		}
		exe, _ := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		left = append(left, fmt.Sprintf("%d %s", pid, filepath.Base(exe)))
	}
	return left
}

// waitHealthy waits for the model to be discovered and healthy.
func waitHealthy(t *testing.T, ctx context.Context, stream *client.Stream, want string) {
	t.Helper()
	for {
		var list struct{ Items []struct{ ID, Health string } }
		if err := stream.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		for _, m := range list.Items {
			if m.ID == want && m.Health == "ok" {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s was never discovered: %+v", want, list)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// usf2Check says whether a task's work holds in dir: the test passes, the README names the
// functions, the rename is complete.
func usf2Check(t *testing.T, task, dir string) string {
	t.Helper()
	var out []string
	run := func(name string, args ...string) string {
		cmd := exec.CommandContext(t.Context(), name, args...)
		cmd.Dir = dir
		cmd.Env = append(append(os.Environ(), offlineGoEnv(t)...), "GOWORK=off")
		b, err := cmd.CombinedOutput()
		if err != nil {
			return "FAIL " + oneLineSummary(string(b))
		}
		return "ok"
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
	if task == "fix" || task == "all" {
		out = append(out, "go test: "+run("go", "test", "./..."))
	}
	if task == "readme" || task == "all" {
		r := read("README.md")
		out = append(out, fmt.Sprintf("README names Max %v, Report %v, no Sum %v",
			strings.Contains(r, "Max"), strings.Contains(r, "Report"), !strings.Contains(r, "Sum(")))
	}
	if task == "rename" || task == "all" {
		src := read("stats.go") + read("report.go")
		out = append(out, fmt.Sprintf("Average defined %v, Mean gone %v, go build: %s",
			strings.Contains(src, "func Average("), !strings.Contains(src, "Mean("), run("go", "build", "./...")))
	}
	return strings.Join(out, "; ")
}

func oneLineSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// compact counts repeated names: ["a", "a", "b"] → ["a×2", "b"].
func compact(names []string) []string {
	var out []string
	for i := 0; i < len(names); {
		j := i
		for j < len(names) && names[j] == names[i] {
			j++
		}
		if j-i > 1 {
			out = append(out, names[i]+"×"+strconv.Itoa(j-i))
		} else {
			out = append(out, names[i])
		}
		i = j
	}
	return out
}
