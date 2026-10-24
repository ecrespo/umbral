package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
