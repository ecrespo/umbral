//go:build live

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// US-003, the reference scenario of PRD §4.1 and §9 (T-F1-21): in a Go repository with a
// broken test, ask "fix it" in an `auto-edit` thread with the failed block attached, approve
// `go test` and nothing else, and end with `go test ./...` green — offline. This file runs it
// UMBRAL_US003_RUNS times (20 by default) against a real daemon and the local model, writes
// the report to UMBRAL_US003_REPORT (default docs/reports/us003-<date>.md) and holds the
// targets: at least 70 % of runs green, and no row in egress_log.
//
//	e2e/us003/run.sh             # the 20 runs and the report
//	UMBRAL_US003_RUNS=2 e2e/us003/run.sh

// us003Fixture is the repository each run starts from: Median picks the upper middle of an
// even sample instead of averaging the two middle values.
const us003Fixture = "../../e2e/us003/fixture"

// us003TestFile is the test the model must make pass without changing it.
const us003TestFile = "stats_test.go"

// us003RunTimeout bounds one run: the block, the turn and the verdict.
const us003RunTimeout = 10 * time.Minute

// us003Run is what one run did.
type us003Run struct {
	N          int
	Green      bool // go test ./... passed afterwards, the test file untouched
	TestEdited bool // the model changed the test instead of the code
	// Changed is every path git reports changed besides stats.go: a new test file with a
	// TestMain that exits 0, or a go.mod edit, can turn the tests green without a fix.
	Changed    []string
	StopReason string // the turn's, or why the run did not reach one
	Duration   time.Duration
	Calls      int // tool calls, one per id
	Deltas     int // thread.delta notifications of the turn's text
	Invalid    int // tool calls recorded invalid_args
	// InvalidCalls is each invalid call's tool and result, for the report: what the model got
	// wrong is what a prompt or a repair would change.
	InvalidCalls []string
	// Tools is every tool the turn called, in order.
	Tools     []string
	Approved  []string
	Denied    []string
	InTokens  int64
	OutTokens int64
	Note      string
}

func TestUS003FixItOffline_REQ_AGT_001(t *testing.T) {
	runs := 20
	if v := os.Getenv("UMBRAL_US003_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("UMBRAL_US003_RUNS=%q", v)
		}
		runs = n
	}
	url := os.Getenv("UMBRAL_OLLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:11434"
	}
	model := os.Getenv("UMBRAL_LIVE_MODEL")
	if model == "" {
		model = "gpt-oss:20b"
	}
	requireModel(t, url, model)

	stream, ctx, dbPath, stop := us003Daemon(t, url, model)
	results := make([]us003Run, 0, runs)
	for i := 1; i <= runs; i++ {
		r := us003Once(t, ctx, stream, i)
		t.Logf("run %d: green=%v stop=%s calls=%d invalid=%d %s %s", i, r.Green, r.StopReason, r.Calls, r.Invalid, r.Duration.Round(time.Second), r.Note)
		results = append(results, r)
	}
	stop(os.Interrupt)

	egress, remote, invalidRows, callRows, failures := us003Audit(t, dbPath)
	report := us003Report(results, model, egress, remote, invalidRows, callRows, failures)
	path := os.Getenv("UMBRAL_US003_REPORT")
	if path == "" {
		path = filepath.Join("..", "..", "docs", "reports", "us003-"+time.Now().Format("2006-01-02")+".md")
	}
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report written to %s", path)

	green := 0
	for _, r := range results {
		if r.Green {
			green++
		}
	}
	// PRD §4.1: ≥ 70 % over 20 runs; Art. 4: nothing left the machine.
	if egress != 0 {
		t.Errorf("egress_log holds %d rows after an offline run", egress)
	}
	if remote != 0 {
		t.Errorf("usage records %d calls to the remote provider, which offline must discard (REQ-LLM-004)", remote)
	}
	// REQ-AGT-001: a turn that answered streamed its answer as thread.delta.
	for _, r := range results {
		if r.StopReason == "end_turn" && r.Deltas == 0 {
			t.Errorf("run %d ended end_turn with no thread.delta", r.N)
		}
	}
	if green*10 < runs*7 {
		t.Errorf("%d of %d runs green, under the 70 %% of PRD §4.1", green, runs)
	}
}

// us003Daemon starts an isolated daemon, offline, whose code class lists a remote candidate
// first: REQ-LLM-004 must discard it without contacting it, and the local model serves.
func us003Daemon(t *testing.T, url, model string) (*client.Stream, context.Context, string, func(os.Signal)) {
	t.Helper()
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
code = ["remote/big-model", "ollama/%[1]s"]
fast = ["remote/big-model", "ollama/%[1]s"]

[[providers]]
id = "remote"
type = "openai-compat"
base_url = "https://remote.invalid/v1"

[[providers]]
id = "ollama"
type = "ollama"
base_url = %[2]q
[providers.options]
num_ctx = 16384
keep_alive = "30m"
`, model, url))

	// The panes' shells and the agent's commands run Go: the developer's build cache keeps
	// them fast, and nothing may fetch a toolchain or a module — offline means the machine too.
	home := t.TempDir()
	env := make([]string, 0, 6)
	env = append(env, "HOME="+home, "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=mod")
	for _, k := range []string{"GOCACHE", "GOMODCACHE"} {
		out, err := exec.CommandContext(t.Context(), "go", "env", k).Output()
		if err != nil {
			t.Fatal(err)
		}
		env = append(env, k+"="+strings.TrimSpace(string(out)))
	}
	stop := startDaemonLoggingTo(t, bin, rt, os.Stderr, env...)

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Hour)
	t.Cleanup(cancel)
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(daemonDir, "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := client.NewStream(c)
	t.Cleanup(func() { _ = stream.Close() })

	want := "ollama/" + model
	for {
		var list struct{ Items []struct{ ID, Health string } }
		if err := stream.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		for _, m := range list.Items {
			if m.ID == want && m.Health == "ok" {
				return stream, ctx, filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db"), stop
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s was never discovered: %+v", want, list)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// us003Once is one run: a fresh copy of the fixture under git, `go test ./...` failing in a
// pane, an auto-edit thread asked to fix it with that block attached, every approval answered,
// and the verdict taken by running the tests again outside the daemon.
func us003Once(t *testing.T, parent context.Context, stream *client.Stream, n int) us003Run {
	t.Helper()
	r := us003Run{N: n}
	ctx, cancel := context.WithTimeout(parent, us003RunTimeout)
	defer cancel()

	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dir, os.DirFS(us003Fixture)); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=us003", "-c", "user.email=us003@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "a broken median")
	before := fileSum(t, filepath.Join(dir, us003TestFile))

	blockID, err := us003FailingBlock(ctx, stream, dir)
	if err != nil {
		r.StopReason, r.Note = "no_block", err.Error()
		return r
	}

	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"mode": "auto-edit", "cwd": dir, "title": fmt.Sprintf("us003 run %d", n)}, &thread); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := stream.Call(ctx, "thread.send", map[string]any{
		"thread_id": thread.ID, "text": "fix it",
		"attachments": []map[string]any{{"kind": "block", "ref": blockID}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	us003Turn(ctx, t, stream, thread.ID, &r)
	r.Duration = time.Since(start)

	// The verdict has its own deadline: a turn that ran out the run's has spent ctx.
	vctx, vcancel := context.WithTimeout(parent, 3*time.Minute)
	defer vcancel()
	r.TestEdited = fileSum(t, filepath.Join(dir, us003TestFile)) != before
	status := exec.CommandContext(vctx, "git", "status", "--porcelain", "--untracked-files=all")
	status.Dir = dir
	porcelain, err := status.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(porcelain), "\n"), "\n") {
		if len(line) > 3 && strings.TrimSpace(line[3:]) != "stats.go" {
			r.Changed = append(r.Changed, strings.TrimSpace(line[3:]))
		}
	}
	verify := exec.CommandContext(vctx, "go", "test", "-count=1", "./...")
	verify.Dir = dir
	verify.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=-mod=mod")
	out, err := verify.CombinedOutput()
	r.Green = err == nil && !r.TestEdited && len(r.Changed) == 0
	if err != nil && r.Note == "" {
		r.Note = "go test still fails: " + lastLine(string(out))
	}
	if len(r.Changed) > 0 {
		r.Note = "changed besides stats.go: " + strings.Join(r.Changed, ", ")
	}
	if r.TestEdited {
		r.Note = "the test file was changed"
	}
	return r
}

// us003FailingBlock runs `go test ./...` in a new pane in dir and returns its block once it
// has closed failing — the block the user would attach.
func us003FailingBlock(ctx context.Context, stream *client.Stream, dir string) (string, error) {
	var session struct{ ID string }
	if err := stream.Call(ctx, "session.create", map[string]any{"cols": 120, "rows": 40, "cwd": dir}, &session); err != nil {
		return "", err
	}
	line := base64.StdEncoding.EncodeToString([]byte("go test ./...\n"))
	if err := stream.Call(ctx, "session.input", map[string]any{"session_id": session.ID, "data_b64": line}, nil); err != nil {
		return "", err
	}
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				return "", fmt.Errorf("the stream ended: %w", stream.Err())
			}
			if n.Method != "block.closed" {
				continue
			}
			var b struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				Command   string `json:"command"`
				ExitCode  *int   `json:"exit_code"`
			}
			if n.Decode(&b) != nil || b.SessionID != session.ID || !strings.Contains(b.Command, "go test") {
				continue
			}
			if b.ExitCode == nil || *b.ExitCode == 0 {
				return "", fmt.Errorf("go test in the pane did not fail: exit %v", b.ExitCode)
			}
			return b.ID, nil
		case <-ctx.Done():
			return "", fmt.Errorf("no block closed for go test: %w", ctx.Err())
		}
	}
}

// us003Turn follows the thread's turn to its end. `go test` is approved once; anything else
// the policy asks about is denied, which is what US-003's "approve the diff" leaves to the
// user once auto-edit has written it.
func us003Turn(ctx context.Context, t *testing.T, stream *client.Stream, threadID string, r *us003Run) {
	t.Helper()
	seen := map[string]bool{}
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "thread.delta":
				var d struct {
					ThreadID string `json:"thread_id"`
					Kind     string `json:"kind"`
				}
				if n.Decode(&d) == nil && d.ThreadID == threadID && d.Kind == "text" {
					r.Deltas++
				}
			case "thread.tool_call":
				var tc struct {
					ID            string          `json:"id"`
					ThreadID      string          `json:"thread_id"`
					Tool          string          `json:"tool"`
					Status        string          `json:"status"`
					Args          json.RawMessage `json:"args"`
					ResultSummary *string         `json:"result_summary"`
				}
				if n.Decode(&tc) != nil || tc.ThreadID != threadID {
					continue
				}
				if !seen[tc.ID] {
					seen[tc.ID] = true
					r.Calls++
					r.Tools = append(r.Tools, tc.Tool)
				}
				if tc.Status == "invalid_args" {
					r.Invalid++
					summary := ""
					if tc.ResultSummary != nil {
						summary = *tc.ResultSummary
					}
					r.InvalidCalls = append(r.InvalidCalls, fmt.Sprintf("%s %s → %s", tc.Tool, truncate(string(tc.Args), 120), truncate(summary, 160)))
				}
			case "approval.requested":
				var a struct {
					ID       string `json:"id"`
					ThreadID string `json:"thread_id"`
					Tool     string `json:"tool"`
					Summary  string `json:"summary"`
				}
				if n.Decode(&a) != nil || a.ThreadID != threadID {
					continue
				}
				decision := "deny"
				if a.Tool == "run_command" && us003IsGoTest(a.Summary) {
					decision = "approve"
					r.Approved = append(r.Approved, a.Summary)
				} else {
					r.Denied = append(r.Denied, a.Tool+": "+a.Summary)
				}
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": decision, "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				var end struct {
					ThreadID   string `json:"thread_id"`
					StopReason string `json:"stop_reason"`
					Usage      struct {
						InTokens  int64 `json:"in_tokens"`
						OutTokens int64 `json:"out_tokens"`
					} `json:"usage"`
				}
				if n.Decode(&end) != nil || end.ThreadID != threadID {
					continue
				}
				r.StopReason, r.InTokens, r.OutTokens = end.StopReason, end.Usage.InTokens, end.Usage.OutTokens
				return
			}
		case <-ctx.Done():
			r.StopReason, r.Note = "run_timeout", "the turn did not end within "+us003RunTimeout.String()
			_ = stream.Call(context.WithoutCancel(ctx), "thread.cancel", map[string]any{"thread_id": threadID}, nil)
			return
		}
	}
}

// us003IsGoTest reports whether a command line is `go test` as a user approving it would
// mean it: one command, package patterns inside the repository, and only the flags that change
// what is run or shown. `-exec`, `-toolexec`, `-o`, `-c` and the profile flags run another
// program or write elsewhere, so they are not approved; neither is a compound line nor a
// quote, which the policy's own splitting would read differently.
func us003IsGoTest(line string) bool {
	if strings.ContainsAny(line, ";&|`$<>\n'\"\\") {
		return false
	}
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "go" || f[1] != "test" {
		return false
	}
	withValue := map[string]bool{"-run": true, "-count": true, "-timeout": true}
	bare := map[string]bool{"-v": true, "-race": true, "-short": true, "-failfast": true}
	for i := 2; i < len(f); i++ {
		a := f[i]
		name, _, hasValue := strings.Cut(a, "=")
		switch {
		case bare[a]:
		case withValue[name] && hasValue:
		case withValue[a] && i+1 < len(f):
			i++
		case (a == "." || strings.HasPrefix(a, "./")) && !slices.Contains(strings.Split(a, "/"), ".."):
		default:
			return false
		}
	}
	return true
}

// us003Audit reads, after the daemon stopped, the rows the targets are judged on: egress_log,
// and the run's tool calls by status; and every model call that failed, with the thread's
// title, which says which run it belongs to.
func us003Audit(t *testing.T, path string) (egress, remote, invalid, calls int, failures []string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for q, dst := range map[string]*int{
		`SELECT count(*) FROM egress_log`:                               &egress,
		`SELECT count(*) FROM usage WHERE provider = 'remote'`:          &remote,
		`SELECT count(*) FROM tool_calls WHERE status = 'invalid_args'`: &invalid,
		`SELECT count(*) FROM tool_calls`:                               &calls,
	} {
		if err := db.QueryRowContext(t.Context(), q).Scan(dst); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rows, err := db.QueryContext(t.Context(), `SELECT coalesce(t.title, ''), u.model_id, u.status, coalesce(u.error, '')
		FROM usage u LEFT JOIN threads t ON t.id = u.thread_id WHERE u.status <> 'ok' ORDER BY u.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var title, model, status, msg string
		if err := rows.Scan(&title, &model, &status, &msg); err != nil {
			t.Fatal(err)
		}
		failures = append(failures, fmt.Sprintf("- %s: `%s` %s: %s", title, model, status, strings.ReplaceAll(truncate(msg, 200), "`", "'")))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return egress, remote, invalid, calls, failures
}

// us003Report is the markdown report T-F1-21's Done asks for.
func us003Report(runs []us003Run, model string, egress, remote, invalidRows, callRows int, failures []string) string {
	green, edited, unrepaired := 0, 0, 0
	var durations []time.Duration
	var in, out int64
	for _, r := range runs {
		if r.Green {
			green++
		}
		if r.TestEdited {
			edited++
		}
		// REQ-AGT-006: an invalid call gets one repair, and an invalid retry ends the turn with
		// tool_error. That turn holds the one invalid call the repair did not fix.
		if r.StopReason == "tool_error" && r.Invalid >= 2 {
			unrepaired++
		}
		if r.Duration > 0 {
			durations = append(durations, r.Duration)
		}
		in, out = in+r.InTokens, out+r.OutTokens
	}
	slices.Sort(durations)
	pct := func(p float64) time.Duration {
		if len(durations) == 0 {
			return 0
		}
		return durations[min(len(durations)-1, int(p*float64(len(durations))))].Round(time.Second)
	}
	rate := func(a, b int) string {
		if b == 0 {
			return "—"
		}
		return fmt.Sprintf("%.1f %%", 100*float64(a)/float64(b))
	}
	verdict := "**met**"
	if green*10 < len(runs)*7 || egress != 0 || remote != 0 {
		verdict = "**not met**"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# US-003 live run — %s\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "T-F1-21. The reference scenario of PRD §4.1 and §9, run %d times against a real daemon\n", len(runs))
	b.WriteString("and the local model, by `e2e/us003/run.sh` (`cmd/umbrald/us003_live_test.go`, tag `live`).\n\n")
	b.WriteString("## Setup\n\n")
	fmt.Fprintf(&b, "- **Model:** `ollama/%s` on the local GPU, `num_ctx = 16384`.\n", model)
	b.WriteString("- **Router:** `offline = true`, with a remote candidate (`remote/big-model`, `https://remote.invalid/v1`) listed\n" +
		"  first in the `code` and `fast` classes. REQ-LLM-004 must discard it without contacting it.\n")
	b.WriteString("- **Each run:**\n" +
		"  - a fresh copy of `e2e/us003/fixture` under git;\n" +
		"  - `go test ./...` typed in a new pane, failing;\n" +
		"  - an `auto-edit` thread in the copy, sent `fix it` with that block attached;\n" +
		"  - `run_command` approved once when the line is a single `go test`, anything else asked about denied;\n" +
		"  - the verdict from running `go test -count=1 ./...` again outside the daemon.\n")
	b.WriteString("- **Success:** `go test ./...` green, `stats_test.go` unchanged, and git reporting no path changed but\n" +
		"  `stats.go`. A run that edits the test, adds a file or touches `go.mod` fails.\n")
	b.WriteString("- **Offline:** the panes and the agent run with `GOTOOLCHAIN=local`, `GOPROXY=off` and `GOFLAGS=-mod=mod`.\n")
	b.WriteString("- **Environment:** the daemon has its own XDG directories and `HOME`; the Go build and module caches are\n" +
		"  the developer's, which keeps a run's builds warm. Ollama keeps the model loaded (`keep_alive = \"30m\"`).\n")
	b.WriteString("- **Measures:** \"left after repair\" counts the turns REQ-AGT-006 ended `tool_error` on an invalid\n" +
		"  retry — one unrepaired call each — over every tool call, which is how PRD §4.1 reads \"the rate of invalid\n" +
		"  tool calls after repair\". \"Repairs that failed\" puts the same count over the invalid calls. An invalid\n" +
		"  call whose turn ended for another reason before its retry is not counted. One model is measured.\n\n")
	b.WriteString("## Results\n\n")
	b.WriteString("| Measure | Value | Target |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| Runs green | %d / %d (%s) | ≥ 70 %% (PRD §4.1), ≥ 14/20 (T-F1-21) |\n", green, len(runs), rate(green, len(runs)))
	fmt.Fprintf(&b, "| Rows in `egress_log` | %d | 0 (Art. 4, REQ-SEC-002) |\n", egress)
	fmt.Fprintf(&b, "| Calls to the remote candidate (`usage`) | %d | 0 (REQ-LLM-004) |\n", remote)
	fmt.Fprintf(&b, "| Invalid tool calls, before repair | %d of %d (%s) | — |\n", invalidRows, callRows, rate(invalidRows, callRows))
	fmt.Fprintf(&b, "| Invalid tool calls left after repair | %d of %d calls (%s) | < 5 %% (PRD §4.1) |\n", unrepaired, callRows, rate(unrepaired, callRows))
	fmt.Fprintf(&b, "| Repairs that failed | %d of %d invalid calls (%s) | — |\n", unrepaired, invalidRows, rate(unrepaired, invalidRows))
	fmt.Fprintf(&b, "| Runs that edited the test | %d | — |\n", edited)
	fmt.Fprintf(&b, "| Latency, send to turn end | p50 %s · p90 %s · max %s | — |\n", pct(0.5), pct(0.9), pct(1))
	fmt.Fprintf(&b, "| Tokens | %d in · %d out | — |\n\n", in, out)
	fmt.Fprintf(&b, "Verdict: T-F1-21's Done is %s.\n\n", verdict)
	b.WriteString("## Runs\n\n| # | Green | Stop | Calls | Invalid | Deltas | Time | Tools | Approved | Denied | Note |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %d | %s | `%s` | %d | %d | %d | %s | %s | %s | %s | %s |\n", r.N, yesNo(r.Green), r.StopReason, r.Calls, r.Invalid, r.Deltas,
			r.Duration.Round(time.Second), strings.Join(r.Tools, " "), cell(r.Approved), cell(r.Denied), strings.ReplaceAll(r.Note, "|", `\|`))
	}
	var invalid []string
	for _, r := range runs {
		for _, c := range r.InvalidCalls {
			invalid = append(invalid, fmt.Sprintf("- run %d: `%s`", r.N, strings.ReplaceAll(c, "`", "'")))
		}
	}
	if len(invalid) > 0 {
		b.WriteString("\n## Invalid tool calls\n\n" + strings.Join(invalid, "\n") + "\n")
	}
	if len(failures) > 0 {
		b.WriteString("\n## Model calls that failed (`usage`)\n\n" + strings.Join(failures, "\n") + "\n")
	}
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func cell(xs []string) string {
	if len(xs) == 0 {
		return "—"
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + strings.ReplaceAll(x, "|", `\|`) + "`"
	}
	return strings.Join(out, ", ")
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "missing"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && l != "FAIL" && !strings.HasPrefix(l, "FAIL\t") {
			return l
		}
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestUS003ApprovesOnlyAPlainGoTest holds the harness's approval rule (delta
// `2026-10-us003-e2e`, decision 3): what it approves is what a user approving "go test" means.
func TestUS003ApprovesOnlyAPlainGoTest(t *testing.T) {
	for line, want := range map[string]bool{
		"go test ./...":                          true,
		"go test -v -count=1 ./...":              true,
		"go test -run TestMedian -race .":        true,
		"go test -timeout 30s ./stats":           true,
		"go test":                                true,
		"go vet ./...":                           false,
		"go test ./... && rm -rf ~":              false,
		"go test -exec \"sh -c 'curl x'\" ./...": false,
		"go test -exec=/bin/sh ./...":            false,
		"go test -toolexec=/tmp/x ./...":         false,
		"go test -c -o /tmp/t .":                 false,
		"go test -coverprofile=../../x ./...":    false,
		"go test ../other/...":                   false,
		"go test -run":                           false,
		"gotest ./...":                           false,
	} {
		if got := us003IsGoTest(line); got != want {
			t.Errorf("us003IsGoTest(%q) = %v, want %v", line, got, want)
		}
	}
}
