package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/acp/adapters/sdk/acptest"
	"github.com/ecrespo/umbral/internal/client"
)

// acpDaemon starts a daemon with one `[[acp.agents]]` entry, `fake`, that launches this test
// binary as an ACP agent through the OpenCode manifest. The scripted Ollama is never called:
// a hosted thread's model is its agent's.
func acpDaemon(t *testing.T, extraEnv ...string) (*client.Stream, context.Context) {
	t.Helper()
	return acpDaemonArgs(t, []string{acptest.Arg}, extraEnv...)
}

// acpDaemonArgs is acpDaemon with the fake agent's arguments.
func acpDaemonArgs(t *testing.T, args []string, extraEnv ...string) (*client.Stream, context.Context) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	srv := (&scriptedOllama{}).server(t)
	stream, ctx, _ := startAgentDaemon(t, srv.URL, daemonOpts{extraEnv: extraEnv, settings: fmt.Sprintf(`
[acp]
silence_timeout = 30

[[acp.agents]]
name = "fake"
manifest = "opencode"
command = %q
args = [%s]
`, exe, strings.Join(quoted, ", "))})
	return stream, ctx
}

// acpThread is what these tests read of a Thread.
type acpThread struct {
	ID               string  `json:"id"`
	Agent            *string `json:"agent"`
	EgressVisibility string  `json:"egress_visibility"`
	WorktreeID       string  `json:"worktree_id"`
	AgentUnavailable *struct {
		Reason, Message string
	} `json:"agent_unavailable"`
}

// sendAndFinish sends text to the thread and returns the notifications up to its turn's end.
func sendAndFinish(t *testing.T, ctx context.Context, stream *client.Stream, threadID, text string) []client.Notification {
	t.Helper()
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": threadID, "text": text}, nil); err != nil {
		t.Fatal(err)
	}
	var seen []client.Notification
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			seen = append(seen, n)
			if n.Method == "thread.turn_finished" {
				return seen
			}
		case <-ctx.Done():
			t.Fatalf("the turn did not finish; saw %d notifications", len(seen))
		}
	}
}

// lastAnswer is the thread's last assistant message.
func lastAnswer(t *testing.T, ctx context.Context, stream *client.Stream, threadID string) string {
	t.Helper()
	var got struct {
		Messages []struct{ Role, Content string }
	}
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": threadID, "include_messages": true}, &got); err != nil {
		t.Fatal(err)
	}
	for i := len(got.Messages) - 1; i >= 0; i-- {
		if got.Messages[i].Role == "assistant" {
			return got.Messages[i].Content
		}
	}
	t.Fatalf("no assistant message in %+v", got.Messages)
	return ""
}

func agentReport(t *testing.T, ctx context.Context, stream *client.Stream, threadID string) acptest.Report {
	t.Helper()
	sendAndFinish(t, ctx, stream, threadID, "report")
	var r acptest.Report
	if err := json.Unmarshal([]byte(lastAnswer(t, ctx, stream, threadID)), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestAnACPAgentIsLaunchedInItsWorktree_REQ_ACP_001: a thread created with an agent and a
// worktree runs its agent in the worktree, not in the main copy, with its session opened there;
// the thread says which agent hosts it and that its own traffic is unobserved.
func TestAnACPAgentIsLaunchedInItsWorktree_REQ_ACP_001(t *testing.T) {
	repo, env := gitRepo(t)
	stream, ctx := acpDaemon(t, env...)

	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo, "worktree": true, "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Agent == nil || *thread.Agent != "fake" || thread.EgressVisibility != "unobserved" || thread.AgentUnavailable != nil {
		t.Fatalf("thread.create = %+v", thread)
	}
	var wt struct{ Path string }
	if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": thread.WorktreeID}, &wt); err != nil {
		t.Fatal(err)
	}
	r := agentReport(t, ctx, stream, thread.ID)
	want, err := filepath.EvalSymlinks(wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cwd != want || r.SessionCwd != wt.Path {
		t.Errorf("the agent runs in %q with its session in %q, want the worktree %q", r.Cwd, r.SessionCwd, wt.Path)
	}
	if strings.HasPrefix(r.Cwd, repo+string(filepath.Separator)) || r.Cwd == repo {
		t.Errorf("the agent runs in the main copy %q", r.Cwd)
	}
}

// TestAnACPThreadWithoutAWorktreeRunsInItsDirectory_REQ_ACP_001: without a worktree the agent
// runs in the thread's own directory, a subdirectory of a repository included.
func TestAnACPThreadWithoutAWorktreeRunsInItsDirectory_REQ_ACP_001(t *testing.T) {
	repo, env := gitRepo(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	stream, ctx := acpDaemon(t, env...)

	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": sub, "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	if r := agentReport(t, ctx, stream, thread.ID); r.Cwd != sub || r.SessionCwd != sub {
		t.Errorf("the agent runs in %q with its session in %q, want %q", r.Cwd, r.SessionCwd, sub)
	}

	// A repository that configures the agent itself is refused before anything is written.
	if err := os.WriteFile(filepath.Join(repo, "opencode.json"), []byte(`{"permission":{"bash":"allow"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := stream.Call(ctx, "thread.create", map[string]any{"cwd": sub, "agent": "fake"}, nil)
	var rpcErr *client.Error
	if !errors.As(err, &rpcErr) || rpcErr.DomainCode != "VALIDATION_ERROR" || rpcErr.Reason != "project_config" {
		t.Errorf("thread.create over a repository's agent config: %v, want VALIDATION_ERROR with reason project_config", err)
	}
	err = stream.Call(ctx, "thread.create", map[string]any{"cwd": sub, "agent": "absent"}, nil)
	if !errors.As(err, &rpcErr) || rpcErr.Reason != "unknown_agent" {
		t.Errorf("thread.create with an unknown agent: %v, want reason unknown_agent", err)
	}
}

// TestAnACPThreadStreamsLikeAnyOther_REQ_ACP_002: the agent's thought, text and tool call reach
// clients as thread.delta and thread.tool_call, the turn ends with thread.turn_finished, and
// the answer and the call are persisted: thread.get reads them back like Umbral's own. Its
// attention state goes `working` and back, which is what its pane follows (DD-012).
func TestAnACPThreadStreamsLikeAnyOther_REQ_ACP_002(t *testing.T) {
	stream, ctx := acpDaemon(t)
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	seen := sendAndFinish(t, ctx, stream, thread.ID, "stream")

	var text, reasoning strings.Builder
	var call struct{ Tool, Risk, Status string }
	var stop string
	var attention []string
	for _, n := range seen {
		switch n.Method {
		case "thread.delta":
			var d struct{ Kind, Text string }
			_ = n.Decode(&d)
			if d.Kind == "text" {
				text.WriteString(d.Text)
			} else {
				reasoning.WriteString(d.Text)
			}
		case "thread.tool_call":
			_ = n.Decode(&call)
		case "thread.attention_changed":
			var a struct {
				AttentionState string `json:"attention_state"`
			}
			_ = n.Decode(&a)
			attention = append(attention, a.AttentionState)
		case "thread.turn_finished":
			var f struct {
				StopReason string `json:"stop_reason"`
			}
			_ = n.Decode(&f)
			stop = f.StopReason
		}
	}
	if text.String() != "hello world" || reasoning.String() != "pondering" {
		t.Errorf("deltas: text %q, reasoning %q", text.String(), reasoning.String())
	}
	if call.Tool != "acp:execute" || call.Risk != "Exec" || call.Status != "ok" {
		t.Errorf("the last thread.tool_call is %+v, want acp:execute, Exec, ok", call)
	}
	if stop != "end_turn" {
		t.Errorf("stop_reason %q", stop)
	}
	if len(attention) == 0 || attention[0] != "working" {
		t.Errorf("attention went %v, want working first", attention)
	}
	if got := lastAnswer(t, ctx, stream, thread.ID); got != "hello world" {
		t.Errorf("the persisted answer is %q", got)
	}
}

// TestAnACPAgentErrorEndsTheTurnAndKeepsTheThread_REQ_ACP_006: a crash ends the turn
// `agent_error`, the thread keeps its history, and the next thread.send relaunches the agent.
func TestAnACPAgentErrorEndsTheTurnAndKeepsTheThread_REQ_ACP_006(t *testing.T) {
	stream, ctx := acpDaemon(t)
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	seen := sendAndFinish(t, ctx, stream, thread.ID, "crash")
	var f struct {
		StopReason string `json:"stop_reason"`
	}
	_ = seen[len(seen)-1].Decode(&f)
	if f.StopReason != "agent_error" {
		t.Fatalf("a crash ended the turn %q, want agent_error", f.StopReason)
	}
	if got := lastAnswer(t, ctx, stream, thread.ID); got != "partial" {
		t.Errorf("the thread kept %q of the crashed turn", got)
	}
	sendAndFinish(t, ctx, stream, thread.ID, "again")
	if got := lastAnswer(t, ctx, stream, thread.ID); got != "echo: again" {
		t.Errorf("after a relaunch the agent answered %q", got)
	}
}

// TestAnACPAgentThatDoesNotStartKeepsItsThread_REQ_ACP_001: an agent that exits before its
// handshake leaves the thread created, with `agent_unavailable` saying so, and the next
// thread.send launches it again.
func TestAnACPAgentThatDoesNotStartKeepsItsThread_REQ_ACP_001(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	stream, ctx := acpDaemonArgs(t, []string{acptest.Arg, acptest.FailOnce, marker})
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "fake"}, &thread); err != nil {
		t.Fatalf("thread.create over an agent that does not start: %v, want the thread", err)
	}
	if thread.ID == "" || thread.AgentUnavailable == nil || thread.AgentUnavailable.Reason != "launch_failed" ||
		thread.AgentUnavailable.Message == "" {
		t.Fatalf("thread.create = %+v, want agent_unavailable with reason launch_failed", thread)
	}
	sendAndFinish(t, ctx, stream, thread.ID, "again")
	if got := lastAnswer(t, ctx, stream, thread.ID); got != "echo: again" {
		t.Errorf("the next send answered %q, want the agent relaunched", got)
	}
}

// TestACPTerminalIsAnAuditedBlock_REQ_ACP_005: a hosted agent's terminal is decided as Exec —
// it asks in normal mode, through Umbral's approval queue — and once approved runs in a new
// session the thread owns, as a block with origin agent, the thread's id and via acp; the
// agent reads its exit and output, and its request is a tool call row of its own.
func TestACPTerminalIsAnAuditedBlock_REQ_ACP_005(t *testing.T) {
	stream, ctx := acpDaemon(t)
	dir := t.TempDir()
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": dir, "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": `term printf from-acp`}, nil); err != nil {
		t.Fatal(err)
	}
	asked := ""
	for done := false; !done; {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "approval.requested":
				var a struct{ ID, Tool, Summary string }
				_ = n.Decode(&a)
				asked = a.Tool + " " + a.Summary
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				done = true
			}
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
	if asked != `acp:terminal/create env 'GREETING=a b' printf from-acp` {
		t.Errorf("asked %q, want the terminal's command line", asked)
	}
	if got := lastAnswer(t, ctx, stream, thread.ID); got != "exit=0 output=from-acp truncated=false" {
		t.Errorf("the agent read %q", got)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var origin, blockThread, via, owner, command string
	if err := db.QueryRowContext(ctx, `
		SELECT b.origin, b.thread_id, b.via, s.owner_thread_id, b.command
		FROM blocks b JOIN sessions s ON s.id = b.session_id WHERE b.thread_id = ?`, thread.ID).
		Scan(&origin, &blockThread, &via, &owner, &command); err != nil {
		t.Fatal(err)
	}
	if origin != "agent" || blockThread != thread.ID || via != "acp" || owner != thread.ID || !strings.Contains(command, "from-acp") {
		t.Errorf("block origin %s thread %s via %s in a session of %s, command %q", origin, blockThread, via, owner, command)
	}
	var tool, decision, rowVia, status string
	if err := db.QueryRowContext(ctx, `SELECT tool, decision, via, status FROM tool_calls WHERE thread_id = ? AND tool = 'acp:terminal/create'`, thread.ID).
		Scan(&tool, &decision, &rowVia, &status); err != nil {
		t.Fatal(err)
	}
	if decision != "ask" || rowVia != "acp" || status != "ok" {
		t.Errorf("the terminal's row: decision %s via %s status %s, want ask, acp, ok", decision, rowVia, status)
	}
}

// TestACPFilesAreMediatedInTheDaemon_REQ_ACP_004: against the daemon's own tool registry, an
// `edit` permission inside an auto-edit thread's write root is allowed unasked, the write it
// covers is performed by Umbral, and the read back is ReadOnly; each is a row decided `allow`.
func TestACPFilesAreMediatedInTheDaemon_REQ_ACP_004(t *testing.T) {
	stream, ctx := acpDaemon(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "notes.txt")
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": dir, "agent": "fake", "mode": "auto-edit"}, &thread); err != nil {
		t.Fatal(err)
	}
	for _, n := range sendAndFinish(t, ctx, stream, thread.ID, "edit "+file+" from the agent") {
		if n.Method == "approval.requested" {
			t.Errorf("asked: %s", n.Params)
		}
	}
	if got := lastAnswer(t, ctx, stream, thread.ID); got != "selected once; written; read from the agent" {
		t.Errorf("the agent read %q", got)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "from the agent" {
		t.Errorf("the file holds %q (%v)", b, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, tool := range []string{"acp:edit", "acp:fs/write_text_file", "acp:fs/read_text_file"} {
		var decision string
		if err := db.QueryRowContext(ctx, `SELECT decision FROM tool_calls WHERE thread_id = ? AND tool = ?`, thread.ID, tool).Scan(&decision); err != nil {
			t.Errorf("%s: %v", tool, err)
			continue
		}
		if decision != "allow" {
			t.Errorf("%s decided %q, want allow", tool, decision)
		}
	}
}

// TestACPCancelStopsWithin500ms_REQ_ACP_008: `thread.cancel` on a hosted thread whose agent is
// waiting on a terminal stops that terminal's command within 500 ms — its block closed — and
// the turn ends cancelled.
func TestACPCancelStopsWithin500ms_REQ_ACP_008(t *testing.T) {
	stream, ctx := acpDaemon(t)
	dir := t.TempDir()
	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": dir, "agent": "fake"}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "hold"}, nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Approve the terminal, then wait for its block to open: `sleep 30` is running.
	for open := false; !open; {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method == "approval.requested" {
				var a struct{ ID string }
				_ = n.Decode(&a)
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			}
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("the terminal never started")
		}
		var n int
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM blocks WHERE thread_id = ? AND via = 'acp' AND ended_at IS NULL`, thread.ID).Scan(&n)
		open = n == 1
	}
	cancelled := time.Now().UnixMilli()
	if err := stream.Call(ctx, "thread.cancel", map[string]any{"thread_id": thread.ID}, nil); err != nil {
		t.Fatal(err)
	}
	var stop string
	for stop == "" {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method == "thread.turn_finished" {
				var f struct {
					StopReason string `json:"stop_reason"`
				}
				_ = n.Decode(&f)
				stop = f.StopReason
			}
		case <-ctx.Done():
			t.Fatal("the cancelled turn did not finish")
		}
	}
	if stop != "cancelled" {
		t.Errorf("the turn ended %q, want cancelled", stop)
	}
	var ended sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT ended_at FROM blocks WHERE thread_id = ? AND via = 'acp'`, thread.ID).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	if !ended.Valid {
		t.Fatal("the terminal's block is still open")
	}
	if took := ended.Int64 - cancelled; took > 500 {
		t.Errorf("the terminal's command stopped %d ms after the cancel, want within 500 ms", took)
	}
}

// TestAnACPThreadIsMarkedUnobserved_REQ_ACP_007: a hosted thread is `unobserved` — in the
// `thread.create` answer, in `thread.get` and in its row — and Umbral's own thread is
// `observed`, since Umbral does not see the agent's own traffic.
func TestAnACPThreadIsMarkedUnobserved_REQ_ACP_007(t *testing.T) {
	stream, ctx := acpDaemon(t)
	dir := t.TempDir()
	var hosted, own acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": dir, "agent": "fake"}, &hosted); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": dir}, &own); err != nil {
		t.Fatal(err)
	}
	if hosted.EgressVisibility != "unobserved" || own.EgressVisibility != "observed" {
		t.Errorf("created: hosted %q, own %q; want unobserved and observed", hosted.EgressVisibility, own.EgressVisibility)
	}
	var got acpThread
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": hosted.ID}, &got); err != nil {
		t.Fatal(err)
	}
	if got.EgressVisibility != "unobserved" {
		t.Errorf("thread.get says %q", got.EgressVisibility)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT egress_visibility FROM threads WHERE id = ?`, hosted.ID).Scan(&stored); err != nil || stored != "unobserved" {
		t.Errorf("the row says %q (%v)", stored, err)
	}
}

// TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004: a hosted ACP agent is an agent too.
// Its isolated HOME sits under the daemon's runtime directory, beside the socket and its token,
// so it can find and dial the daemon; when it calls a method only the user may call — here
// `skill.install` — it is refused like a client inside an agent's terminal, and the user's own
// client is not (delta `2026-10-hosted-agent-peer`, from Q12 of `2026-10-hrd02-open-questions`).
func TestAHostedAgentIsRefusedTheUsersMethods_REQ_ORC_004(t *testing.T) {
	stream, ctx := acpDaemon(t)
	var hosted acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "agent": "fake"}, &hosted); err != nil {
		t.Fatal(err)
	}
	sendAndFinish(t, ctx, stream, hosted.ID, "dial skill.install")
	if got := lastAnswer(t, ctx, stream, hosted.ID); got != "PERMISSION_DENIED" {
		t.Errorf("the hosted agent's skill.install answered %q, want PERMISSION_DENIED", got)
	}

	// The refusal is about the caller: the user's own client gets past it to the method.
	err := stream.Call(ctx, "skill.install", map[string]any{}, nil)
	if err == nil || client.DomainCode(err) == "PERMISSION_DENIED" {
		t.Errorf("the user's skill.install answered %v, want an error other than PERMISSION_DENIED", err)
	}
}
