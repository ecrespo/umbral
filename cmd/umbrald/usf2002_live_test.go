//go:build live

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// US-F2-002 (PRD F2 §9): the agent answers a `psql` prompt and a `git rebase -i` editor through
// terminal_interact. PRD F2 §4 sets ≥ 70 % over 10 runs with gpt-oss:20b. Postgres runs in a
// container the test owns; `psql` is the container's, reached with `docker exec -it` from the
// thread's own terminal, so the host needs docker and nothing else. UMBRAL_USF2002_RUNS changes
// the number of runs and UMBRAL_USF2002_REPORT where the report goes.

// usf2002TaskTimeout bounds one task of one run.
const usf2002TaskTimeout = 10 * time.Minute

// usf2002Run is one run's two tasks.
type usf2002Run struct {
	N            int
	PSQL, Rebase usf2002Task
	Green        bool
	Duration     time.Duration
}

// usf2002Task is what one task's turn did and whether its end state is the asked one.
type usf2002Task struct {
	Green      bool
	StopReason string
	Calls      int
	Keys       int
	Approved   int
	Denied     []string
	Note       string
	Duration   time.Duration
	// Log is the turn's transcript: each finished call and the model's text, for a failure's cause.
	Log []string
}

func TestUSF2002InteractivePrograms_REQ_FTU_001(t *testing.T) {
	runs := 10
	if v := os.Getenv("UMBRAL_USF2002_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("UMBRAL_USF2002_RUNS=%q", v)
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
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	requireModel(t, url, model)
	requireIdleModel(t, url, model)

	pg := usf2002Postgres(t)
	stream, ctx := usf2002Daemon(t, url, model, runs)

	results := make([]usf2002Run, 0, runs)
	for i := 1; i <= runs; i++ {
		start := time.Now()
		r := usf2002Run{N: i}
		r.PSQL = usf2002PSQL(t, ctx, stream, pg, i)
		r.Rebase = usf2002Rebase(t, ctx, stream, i)
		r.Green = r.PSQL.Green && r.Rebase.Green
		r.Duration = time.Since(start)
		t.Logf("run %d: psql %v (%s, %s), rebase %v (%s, %s), %s", i,
			r.PSQL.Green, r.PSQL.StopReason, r.PSQL.Note, r.Rebase.Green, r.Rebase.StopReason, r.Rebase.Note, r.Duration.Round(time.Second))
		results = append(results, r)
		if dir := os.Getenv("UMBRAL_USF2002_TRANSCRIPTS"); dir != "" {
			body := "# psql\n" + strings.Join(r.PSQL.Log, "\n") + "\n\n# rebase\n" + strings.Join(r.Rebase.Log, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("run-%d.txt", i)), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	report := usf2002Report(results, model)
	path := os.Getenv("UMBRAL_USF2002_REPORT")
	if path == "" {
		path = filepath.Join("..", "..", "docs", "reports", "usf2-002-"+time.Now().Format("2006-01-02")+".md")
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
	if green*10 < runs*7 {
		t.Errorf("%d of %d runs green, under the 70 %% of PRD F2 §4", green, runs)
	}
}

// usf2002Postgres starts a throwaway Postgres container, waits for it to accept a query, and
// removes it when the test ends.
func usf2002Postgres(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("umbral-usf2002-%d", os.Getpid())
	out, err := exec.CommandContext(t.Context(), "docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_PASSWORD=umbral", "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:16-alpine").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run postgres: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := usf2002Query(name, "select 1"); err == nil {
			return name
		}
		if time.Now().After(deadline) {
			t.Fatal("postgres did not accept a query within 60 s")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// usf2002Query runs one query in the container, outside the agent's terminal, unaligned.
func usf2002Query(container, query string) (string, error) {
	out, err := exec.Command("docker", "exec", container, "psql", "-U", "postgres", "-tAc", query).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// usf2002Daemon starts an isolated daemon whose classes are the live model, with bash as the
// shell and an empty home, so the thread's terminal reads no rc file of the developer's.
func usf2002Daemon(t *testing.T, url, model string, runs int) (*client.Stream, context.Context) {
	t.Helper()
	bin := buildDaemon(t)
	rt, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "models.toml"), fmt.Sprintf(`
[classes]
code = ["ollama/%[1]s"]
fast = ["ollama/%[1]s"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = %[2]q
[providers.options]
num_ctx = 32768
keep_alive = "30m"
`, model, url))
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	// The editor is named outright: a harness that runs this test may export GIT_EDITOR=true
	// (Claude Code does), and git, which reads it before EDITOR, would then rebase without
	// opening vim — every rebase run before this line was that.
	startDaemon(t, bin, rt, "SHELL="+bash, "HOME="+t.TempDir(), "EDITOR=vim", "VISUAL=vim", "GIT_EDITOR=vim")

	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(runs)*2*usf2002TaskTimeout+5*time.Minute)
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
				return stream, ctx
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s was never discovered", want)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// usf2002PSQL is the psql task: open psql in the thread's terminal, create a table with three
// rows, quit. Green when the rows are there and no psql is left running.
func usf2002PSQL(t *testing.T, parent context.Context, stream *client.Stream, pg string, n int) usf2002Task {
	t.Helper()
	table := fmt.Sprintf("items_%d", n)
	if _, err := usf2002Query(pg, "drop table if exists "+table); err != nil {
		t.Fatal(err)
	}
	prompt := fmt.Sprintf("Answer the prompts in your terminal with the terminal_interact tool; do not use run_command. "+
		"Start psql by typing `docker exec -it %s psql -U postgres` and enter. At the psql prompt create a table named %s "+
		"with one integer column id, insert the rows 1, 2 and 3, then quit psql with \\q. Read the screen to check each step.", pg, table)
	task := usf2002Turn(t, parent, stream, t.TempDir(), prompt)
	got, err := usf2002Query(pg, "select string_agg(id::text, ',' order by id) from "+table)
	switch {
	case err != nil:
		task.Note = "no table: " + truncate(got, 80)
	case got != "1,2,3":
		task.Note = "rows " + got
	default:
		left, _ := exec.Command("docker", "exec", pg, "sh", "-c", "pgrep -x psql || true").CombinedOutput()
		if strings.TrimSpace(string(left)) != "" {
			task.Note = "psql left running"
		} else {
			task.Green = task.StopReason == "end_turn"
			if !task.Green {
				task.Note = "rows right, turn " + task.StopReason
			}
		}
	}
	return task
}

// usf2002Rebase is the rebase task: a repository whose last three commits are squashed into one
// with `git rebase -i` in vim. Green when two commits remain, the work of all three is in the
// last, no rebase is in progress and the turn ended.
func usf2002Rebase(t *testing.T, parent context.Context, stream *client.Stream, n int) usf2002Task {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "US-F2-002")
	git("config", "user.email", "usf2002@example.invalid")
	git("config", "core.editor", "vim")
	git("config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(repo, "notes.txt"), "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	for _, word := range []string{"one", "two", "three"} {
		f, err := os.OpenFile(filepath.Join(repo, "notes.txt"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(word + "\n")
		_ = f.Close()
		git("commit", "-q", "-am", "add "+word)
	}
	prompt := "Answer the prompts in your terminal with the terminal_interact tool; do not use run_command. " +
		"In this repository squash the last three commits into one: type `git rebase -i HEAD~3` and enter. " +
		"The editor is vim: change `pick` to `squash` on the second and third lines, then save and quit with :wq. " +
		"When vim opens the commit message, save and quit it with :wq too. Read the screen to check each step."
	task := usf2002Turn(t, parent, stream, repo, prompt)
	_, rebasing := os.Stat(filepath.Join(repo, ".git", "rebase-merge"))
	count := git("rev-list", "--count", "HEAD")
	content, _ := os.ReadFile(filepath.Join(repo, "notes.txt"))
	switch {
	case rebasing == nil:
		task.Note = "rebase left in progress"
	case count != "2":
		task.Note = count + " commits"
	case string(content) != "base\none\ntwo\nthree\n":
		task.Note = "notes.txt changed"
	default:
		task.Green = task.StopReason == "end_turn"
		if !task.Green {
			task.Note = "squashed, turn " + task.StopReason
		}
	}
	return task
}

// usf2002Turn creates a thread in cwd, sends prompt and follows its turn. terminal_interact's
// asks are approved, except a destructive one; every other tool's ask is denied, since the task
// is to drive the terminal.
func usf2002Turn(t *testing.T, parent context.Context, stream *client.Stream, cwd, prompt string) usf2002Task {
	t.Helper()
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, usf2002TaskTimeout)
	defer cancel()
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": prompt}, nil); err != nil {
		t.Fatal(err)
	}
	var task usf2002Task
	seen := map[string]bool{}
	// pending is each call announced and not yet finished, and reasoning how much the model
	// thought: what a turn that does not end was doing.
	pending := map[string]string{}
	reasoning := 0
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			task.Log = append(task.Log, "model: "+text.String())
			text.Reset()
		}
	}
	defer func() { task.Duration = time.Since(start) }()
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
					Text     string `json:"text"`
				}
				if n.Decode(&d) == nil && d.ThreadID == thread.ID {
					if d.Kind == "text" {
						text.WriteString(d.Text)
					} else {
						reasoning += len(d.Text)
					}
				}
			case "thread.tool_call":
				var tc struct {
					ID       string         `json:"id"`
					ThreadID string         `json:"thread_id"`
					Tool     string         `json:"tool"`
					Args     map[string]any `json:"args"`
					Status   string         `json:"status"`
					Result   string         `json:"result_summary"`
				}
				if n.Decode(&tc) != nil || tc.ThreadID != thread.ID {
					continue
				}
				if tc.Status == "pending" {
					pending[tc.ID] = fmt.Sprintf("%s %v (%s since %s)", tc.Tool, tc.Args, tc.Status, time.Since(start).Round(time.Second))
				} else {
					delete(pending, tc.ID)
				}
				if tc.Status != "pending" {
					flush()
					task.Log = append(task.Log, fmt.Sprintf("call %s %v -> %s: %s", tc.Tool, tc.Args, tc.Status, tc.Result))
				}
				if seen[tc.ID] {
					continue
				}
				seen[tc.ID] = true
				task.Calls++
				if tc.Tool == "terminal_interact" && tc.Args["action"] == "send_keys" {
					task.Keys++
				}
			case "approval.requested":
				var a struct {
					ID       string `json:"id"`
					ThreadID string `json:"thread_id"`
					Tool     string `json:"tool"`
					Reason   string `json:"reason"`
					Summary  string `json:"summary"`
				}
				if n.Decode(&a) != nil || a.ThreadID != thread.ID {
					continue
				}
				// The harness is a user reading each ask: a key the matcher does not model —
				// vim's escape — asks on the destructive floor, and a user who sees `<escape>`
				// approves it; one whose keys hold a destructive command does not.
				decision := "deny"
				if a.Tool == "terminal_interact" && !usf2002Dangerous.MatchString(a.Summary) {
					decision = "approve"
					task.Approved++
				} else {
					task.Denied = append(task.Denied, a.Tool+": "+truncate(a.Summary, 80))
				}
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": decision, "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				var end struct {
					ThreadID   string `json:"thread_id"`
					StopReason string `json:"stop_reason"`
				}
				if n.Decode(&end) != nil || end.ThreadID != thread.ID {
					continue
				}
				flush()
				task.StopReason = end.StopReason
				task.Log = append(task.Log, usf2002ToolResults(ctx, stream, thread.ID)...)
				return task
			}
		case <-ctx.Done():
			flush()
			task.StopReason, task.Note = "run_timeout", "the turn did not end within "+usf2002TaskTimeout.String()
			for _, p := range pending {
				task.Log = append(task.Log, "still pending: "+p)
			}
			task.Log = append(task.Log, fmt.Sprintf("reasoning: %d bytes", reasoning))
			_ = stream.Call(context.WithoutCancel(ctx), "thread.cancel", map[string]any{"thread_id": thread.ID}, nil)
			task.Log = append(task.Log, usf2002ToolResults(context.WithoutCancel(ctx), stream, thread.ID)...)
			return task
		}
	}
}

// usf2002ToolResults is what each tool call answered the model, for the transcript: the
// notifications carry a summary only, and a failure is read from what the model was shown.
// usf2002Dangerous is what the harness, as the user, never approves in a summary.
var usf2002Dangerous = regexp.MustCompile(`(?i)\brm\s+-|\bdrop\s|\btruncate\b|\bdelete\s+from\b|--force|\breset\s+--hard|\bpush\b|\bmkfs|\bdd\s+if=|:!`)

func usf2002ToolResults(ctx context.Context, _ *client.Stream, threadID string) []string {
	// The model reads a call's whole result, which only the database keeps (result_json).
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return []string{"results: " + err.Error()}
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT args_json, status, COALESCE(result_json, '') FROM tool_calls WHERE thread_id = ? ORDER BY started_at`, threadID)
	if err != nil {
		return []string{"results: " + err.Error()}
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var args, status, result string
		if rows.Scan(&args, &status, &result) == nil {
			out = append(out, fmt.Sprintf("result of %s (%s):\n%s", truncate(args, 300), status, truncate(result, 2500)))
		}
	}
	return out
}

func usf2002Report(runs []usf2002Run, model string) string {
	var b strings.Builder
	green, psql, rebase := 0, 0, 0
	for _, r := range runs {
		if r.Green {
			green++
		}
		if r.PSQL.Green {
			psql++
		}
		if r.Rebase.Green {
			rebase++
		}
	}
	fmt.Fprintf(&b, "# US-F2-002 live run — %s\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "`TestUSF2002InteractivePrograms_REQ_FTU_001` (`cmd/umbrald/usf2002_live_test.go`, tag `live`), model `%s` on the local Ollama, ", model)
	b.WriteString("a real daemon with bash as the shell and an empty home, Postgres 16 in a container the test owns, and vim as git's editor. ")
	b.WriteString("Each run is a fresh thread for each task. `terminal_interact` asks were approved unless destructive; every other tool's ask was denied.\n\n")
	fmt.Fprintf(&b, "**%d of %d runs green** (PRD F2 §4: ≥ 70 %%): psql %d/%d, rebase %d/%d.\n\n", green, len(runs), psql, len(runs), rebase, len(runs))
	b.WriteString("| Run | Green | psql | stop | calls | keys | note | rebase | stop | calls | keys | note | time |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | %d | %s | %s | %s | %d | %d | %s | %s |\n", r.N, yesNo(r.Green),
			yesNo(r.PSQL.Green), r.PSQL.StopReason, r.PSQL.Calls, r.PSQL.Keys, cell(append([]string{r.PSQL.Note}, r.PSQL.Denied...)),
			yesNo(r.Rebase.Green), r.Rebase.StopReason, r.Rebase.Calls, r.Rebase.Keys, cell(append([]string{r.Rebase.Note}, r.Rebase.Denied...)),
			r.Duration.Round(time.Second))
	}
	return b.String()
}
