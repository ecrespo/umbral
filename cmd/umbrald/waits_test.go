package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// The wait engine through a real daemon (T-F1-23). client.Stream answers one call at a time,
// so a test that acts while a wait is pending makes its wait on a second connection.

func writeFileCall(path string) string {
	return fmt.Sprintf(`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":%q,"content":"x\n"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`, path)
}

const doneReply = `{"model":"m","message":{"role":"assistant","content":"Done."},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`

// secondStream opens another TUI connection to the daemon agentDaemon started.
func secondStream(t *testing.T, ctx context.Context) *client.Stream {
	t.Helper()
	c, err := client.Connect(ctx, client.Options{
		SocketPath:  filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "umbral", "umbral.sock"),
		NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := client.NewStream(c)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type waitAnswer struct {
	ThreadID string  `json:"thread_id"`
	TurnID   *string `json:"turn_id"`
	State    string  `json:"state"`
}

// threadWait runs thread.wait on s in the background.
func threadWait(ctx context.Context, s *client.Stream, threadID string, until []string, timeoutMS int) <-chan struct {
	got waitAnswer
	err error
} {
	out := make(chan struct {
		got waitAnswer
		err error
	}, 1)
	go func() {
		var got waitAnswer
		err := s.Call(ctx, "thread.wait", map[string]any{"thread_id": threadID, "until": until, "timeout_ms": timeoutMS}, &got)
		out <- struct {
			got waitAnswer
			err error
		}{got, err}
	}()
	return out
}

// blockedThread starts a thread whose first turn pauses on write_file's approval, and
// returns it with the approval's id and the turn's.
func blockedThread(t *testing.T, stream *client.Stream, ctx context.Context) (threadID, approvalID, turnID string) {
	t.Helper()
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		TurnID string `json:"turn_id"`
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "write it"}, &sent); err != nil {
		t.Fatal(err)
	}
	var a struct{ ID string }
	awaitNotification(t, stream, "approval.requested", &a)
	return thread.ID, a.ID, sent.TurnID
}

// TestWaitPinsTurn_REQ_AUT_001: a wait started while turn A is paused is pinned to A. A ends
// `done`, which is not a target and cannot become one; the wait answers then, with A, and
// turn B — cancelled into `stopped`, which is a target — never satisfies it.
func TestWaitPinsTurn_REQ_AUT_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{{writeFileCall("a.txt")}, {doneReply}, {writeFileCall("b.txt")}}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)
	threadID, approvalID, turnA := blockedThread(t, stream, ctx)

	waiter := secondStream(t, ctx)
	answer := threadWait(ctx, waiter, threadID, []string{"stopped"}, 30000)
	time.Sleep(100 * time.Millisecond) // the wait is pinned before A moves on

	if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": approvalID, "decision": "approve", "scope": "once"}, nil); err != nil {
		t.Fatal(err)
	}
	awaitNotification(t, stream, "thread.turn_finished", nil)
	// Turn B: paused, then cancelled — the thread ends `stopped`.
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": threadID, "text": "again"}, nil); err != nil {
		t.Fatal(err)
	}
	awaitNotification(t, stream, "approval.requested", nil)
	if err := stream.Call(ctx, "thread.cancel", map[string]any{"thread_id": threadID}, nil); err != nil {
		t.Fatal(err)
	}

	r := <-answer
	if r.err != nil || r.got.State != "done" || r.got.TurnID == nil || *r.got.TurnID != turnA || r.got.ThreadID != threadID {
		t.Fatalf("thread.wait = %+v %v, want turn %s done", r.got, r.err, turnA)
	}

	// A wait on a thread already in a target returns at once, with its latest turn.
	start := time.Now()
	r = <-threadWait(ctx, waiter, threadID, []string{"stopped"}, 30000)
	if r.err != nil || r.got.State != "stopped" || r.got.TurnID == nil || *r.got.TurnID == turnA || time.Since(start) > 2*time.Second {
		t.Fatalf("thread.wait on a stopped thread = %+v %v after %v", r.got, r.err, time.Since(start))
	}
}

// TestSendWaitPinsItsTurnAndReturnsFinalState_REQ_AUT_001: thread.send with `wait` answers
// once its own turn has settled, with final_state.
func TestSendWaitPinsItsTurnAndReturnsFinalState_REQ_AUT_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{{doneReply}}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)
	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	var got struct {
		TurnID     string  `json:"turn_id"`
		FinalState *string `json:"final_state"`
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{
		"thread_id": thread.ID, "text": "hi",
		"wait": map[string]any{"until": []string{"done", "idle"}, "timeout_ms": 30000},
	}, &got); err != nil {
		t.Fatal(err)
	}
	var th struct{ State string }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &th); err != nil {
		t.Fatal(err)
	}
	if got.FinalState == nil || *got.FinalState != "done" || got.TurnID == "" || th.State != "idle" {
		t.Fatalf("thread.send with wait = %+v (final %v), thread %s", got, got.FinalState, th.State)
	}
}

// TestSendWaitRejectsBlocked_REQ_AUT_002: a send with `wait` on a thread paused on an
// approval is THREAD_BLOCKED; no message is persisted and the model is not called.
func TestSendWaitRejectsBlocked_REQ_AUT_002(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{{writeFileCall("a.txt")}}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)
	threadID, _, _ := blockedThread(t, stream, ctx)

	var before struct{ Messages []struct{ ID string } }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": threadID, "include_messages": true}, &before); err != nil {
		t.Fatal(err)
	}
	err := stream.Call(ctx, "thread.send", map[string]any{
		"thread_id": threadID, "text": "more",
		"wait": map[string]any{"until": []string{"done"}, "timeout_ms": 30000},
	}, nil)
	var rpcErr *client.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32010 || rpcErr.DomainCode != "THREAD_BLOCKED" {
		t.Fatalf("thread.send with wait on a blocked thread = %v, want THREAD_BLOCKED", err)
	}
	var after struct {
		State    string
		Messages []struct{ ID string }
	}
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": threadID, "include_messages": true}, &after); err != nil {
		t.Fatal(err)
	}
	ollama.mu.Lock()
	calls := len(ollama.bodies)
	ollama.mu.Unlock()
	if len(after.Messages) != len(before.Messages) || after.State != "awaiting_approval" || calls != 1 {
		t.Fatalf("after the refusal: %d messages (was %d), state %s, %d model calls", len(after.Messages), len(before.Messages), after.State, calls)
	}
}

// TestWaitTimeoutReportsLastState_REQ_AUT_004: a wait that reaches its deadline is TIMEOUT
// with the last state it saw, and nothing is resent: thread.wait and thread.send's wait both
// leave the thread, its messages and the model's calls as they were.
func TestWaitTimeoutReportsLastState_REQ_AUT_004(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{{writeFileCall("a.txt")}, {writeFileCall("b.txt")}}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)
	threadID, _, _ := blockedThread(t, stream, ctx)

	start := time.Now()
	err := stream.Call(ctx, "thread.wait", map[string]any{"thread_id": threadID, "until": []string{"done"}, "timeout_ms": 1000}, nil)
	var rpcErr *client.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32011 || rpcErr.DomainCode != "TIMEOUT" || rpcErr.LastState != "blocked" {
		t.Fatalf("thread.wait = %v, want TIMEOUT with last_state blocked", err)
	}
	if took := time.Since(start); took < time.Second {
		t.Fatalf("timed out after %v", took)
	}

	// A send with a wait whose own turn pauses: the message went once, and stays once.
	var other struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &other); err != nil {
		t.Fatal(err)
	}
	err = stream.Call(ctx, "thread.send", map[string]any{
		"thread_id": other.ID, "text": "write b",
		"wait": map[string]any{"until": []string{"done"}, "timeout_ms": 1000},
	}, nil)
	if !errors.As(err, &rpcErr) || rpcErr.DomainCode != "TIMEOUT" || rpcErr.LastState != "blocked" {
		t.Fatalf("thread.send with wait = %v, want TIMEOUT with last_state blocked", err)
	}
	var got struct{ Messages []struct{ Role string } }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": other.ID, "include_messages": true}, &got); err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range got.Messages {
		if m.Role == "user" {
			users++
		}
	}
	ollama.mu.Lock()
	calls := len(ollama.bodies)
	ollama.mu.Unlock()
	if users != 1 || calls != 2 {
		t.Fatalf("after the timeout: %d user messages, %d model calls; want 1 and 2", users, calls)
	}
}

// TestWaitOutputMatchesLine_REQ_AUT_003: block.wait_output evaluates the pane's recent output
// line by line — what was already on screen, then what follows — and returns the first
// matching line with its block.
func TestWaitOutputMatchesLine_REQ_AUT_003(t *testing.T) {
	stream, ctx := agentDaemon(t, (&scriptedOllama{}).server(t).URL)
	var session struct{ ID string }
	if err := stream.Call(ctx, "session.create", map[string]any{"cols": 100, "rows": 30, "cwd": t.TempDir()}, &session); err != nil {
		t.Fatal(err)
	}
	input := func(line string) {
		t.Helper()
		if err := stream.Call(ctx, "session.input", map[string]any{"session_id": session.ID, "data_b64": base64.StdEncoding.EncodeToString([]byte(line + "\n"))}, nil); err != nil {
			t.Fatal(err)
		}
	}
	type outputAnswer struct {
		BlockID     *string `json:"block_id"`
		MatchedLine string  `json:"matched_line"`
		LineNumber  int     `json:"line_number"`
	}

	// Already on screen when the wait starts. The command line itself, `echo before-$((…`,
	// does not match.
	input("echo before-$((40+2))")
	waiter := secondStream(t, ctx)
	var got outputAnswer
	if err := waiter.Call(ctx, "block.wait_output", map[string]any{"session_id": session.ID, "regex": `^before-\d+$`, "timeout_ms": 15000}, &got); err != nil {
		t.Fatal(err)
	}
	if got.MatchedLine != "before-42" || got.LineNumber < 1 {
		t.Fatalf("block.wait_output on the screen = %+v", got)
	}

	// Output that follows the start, line by line: the first match is returned.
	answer := make(chan error, 1)
	go func() {
		answer <- waiter.Call(ctx, "block.wait_output", map[string]any{"session_id": session.ID, "regex": `^after-[0-9]+$`, "timeout_ms": 15000}, &got)
	}()
	time.Sleep(200 * time.Millisecond)
	input("printf 'noise\\nafter-%s\\nafter-%s\\n' 7 8")
	if err := <-answer; err != nil {
		t.Fatal(err)
	}
	if got.MatchedLine != "after-7" || got.BlockID == nil {
		t.Fatalf("block.wait_output on live output = %+v", got)
	}
	var block struct {
		SessionID string `json:"session_id"`
	}
	if err := stream.Call(ctx, "block.get", map[string]any{"block_id": *got.BlockID}, &block); err != nil || block.SessionID != session.ID {
		t.Fatalf("the matched line's block %s: %+v %v", *got.BlockID, block, err)
	}
}
