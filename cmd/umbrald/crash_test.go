package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// TestCrashRecovery_REQ_AGT_011 kills the daemon with SIGKILL twice — once while a turn waits
// for an approval, once while the model is still streaming — and restarts it on the same
// database each time (T-F1-22). REQ-AGT-011 persists before it announces, so after each crash
// everything the client was shown is in the store: the user's message, the streamed text, the
// tool call and its approval. Data Model §6 then leaves the thread `stopped` and the approval
// `expired`, nothing the approval guarded has run, and the thread takes `thread.send` again
// with its whole history (REQ-AGT-017).
func TestCrashRecovery_REQ_AGT_011(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		// Turn 1: some text, then a write that normal mode asks about.
		{`{"model":"m","message":{"role":"assistant","content":"Looking at it.","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"out.txt","content":"x\n"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
		// Turn 2: half an answer, then the model is still generating when the daemon dies.
		{`{"model":"m","message":{"role":"assistant","content":"Partial answer, "},"done":false}`, scriptedHold},
		// Turn 3, after the second restart.
		{`{"model":"m","message":{"role":"assistant","content":"All done."},"done":true,"done_reason":"stop","prompt_eval_count":30,"eval_count":3}`},
	}}
	srv := ollama.server(t)
	stream, ctx, stop := startAgentDaemon(t, srv.URL, daemonOpts{})
	bin := buildDaemon(t)
	cwd := t.TempDir()

	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	send := func(s *client.Stream, text string) {
		t.Helper()
		if err := s.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": text}, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Crash 1: the turn is awaiting an approval the client was shown.
	send(stream, "write the file")
	seen := crashUntil(ctx, t, stream, func(method string) bool { return method == "approval.requested" })
	if !strings.Contains(seen, "Looking at it.") {
		t.Fatalf("the client saw %q before the approval", seen)
	}
	stop(os.Kill)

	stop = startDaemonLoggingTo(t, bin, os.Getenv("XDG_RUNTIME_DIR"), os.Stderr)
	stream = reconnectAgent(ctx, t)
	got := crashThread(ctx, t, stream, thread.ID)
	if got.State != "stopped" {
		t.Errorf("after the crash the thread is %q, want stopped (Data Model §6 step 3)", got.State)
	}
	if !got.has("user", "write the file") || !got.has("assistant", "Looking at it.") {
		t.Fatalf("what the client was shown is not all in the store: %+v", got.Messages)
	}
	var approvals struct {
		Items []struct{ Tool, State string }
	}
	if err := stream.Call(ctx, "approval.list", map[string]any{"thread_id": thread.ID, "all": true}, &approvals); err != nil {
		t.Fatal(err)
	}
	if len(approvals.Items) != 1 || approvals.Items[0].Tool != "write_file" || approvals.Items[0].State != "expired" {
		t.Fatalf("the approval after the crash: %+v, want one write_file, expired (step 4)", approvals.Items)
	}
	if calls := crashToolCalls(ctx, t, thread.ID); calls != 1 {
		t.Errorf("%d tool call rows after the crash, want the one the approval was for", calls)
	}
	if _, err := os.Stat(filepath.Join(cwd, "out.txt")); err == nil {
		t.Fatal("the write the crash interrupted ran without its approval")
	}

	// Crash 2: the model is mid-stream. Every delta the client got is already a row.
	send(stream, "go on")
	seen = crashUntil(ctx, t, stream, func(method string) bool { return false })
	if !strings.Contains(seen, "Partial answer, ") {
		t.Fatalf("the client saw %q", seen)
	}
	stop(os.Kill)

	startDaemonLoggingTo(t, bin, os.Getenv("XDG_RUNTIME_DIR"), os.Stderr)
	stream = reconnectAgent(ctx, t)
	got = crashThread(ctx, t, stream, thread.ID)
	if got.State != "stopped" || !got.has("assistant", "Partial answer, ") {
		t.Fatalf("after the second crash: state %q, messages %+v", got.State, got.Messages)
	}

	// The thread resumes with its whole history.
	send(stream, "finish")
	crashUntil(ctx, t, stream, func(method string) bool { return method == "thread.turn_finished" })
	got = crashThread(ctx, t, stream, thread.ID)
	if got.State != "idle" || !got.has("assistant", "All done.") {
		t.Fatalf("the resumed turn: state %q, messages %+v", got.State, got.Messages)
	}
	ollama.mu.Lock()
	last := ollama.bodies[len(ollama.bodies)-1]
	ollama.mu.Unlock()
	for _, want := range []string{"write the file", "Looking at it.", "go on", "Partial answer, ", "finish"} {
		if !strings.Contains(last, want) {
			t.Errorf("the resumed turn's request lost %q from the history", want)
		}
	}
}

// crashUntil reads notifications, gathering the text deltas, until stop says a method ends the
// wait; a stop that never fires returns once some text has arrived and the stream is quiet.
func crashUntil(ctx context.Context, t *testing.T, s *client.Stream, stop func(method string) bool) string {
	t.Helper()
	var text strings.Builder
	quiet := time.NewTimer(time.Hour)
	defer quiet.Stop()
	for {
		select {
		case n, ok := <-s.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", s.Err())
			}
			if n.Method == "thread.delta" {
				var d struct{ Kind, Text string }
				_ = n.Decode(&d)
				if d.Kind == "text" {
					text.WriteString(d.Text)
					quiet.Reset(500 * time.Millisecond)
				}
			}
			if stop(n.Method) {
				return text.String()
			}
		case <-quiet.C:
			return text.String()
		case <-ctx.Done():
			t.Fatalf("waited in vain; text so far %q", text.String())
		}
	}
}

type crashState struct {
	State    string
	Messages []struct{ Role, Content string }
}

func (c crashState) has(role, text string) bool {
	for _, m := range c.Messages {
		if m.Role == role && strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

func crashThread(ctx context.Context, t *testing.T, s *client.Stream, id string) crashState {
	t.Helper()
	var got crashState
	if err := s.Call(ctx, "thread.get", map[string]any{"thread_id": id, "include_messages": true}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func crashToolCalls(ctx context.Context, t *testing.T, threadID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tool_calls WHERE thread_id = ?`, threadID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// reconnectAgent connects to a daemon restarted on the runtime, data and config directories
// the test's first daemon used, and waits for the model to be back.
func reconnectAgent(ctx context.Context, t *testing.T) *client.Stream {
	t.Helper()
	rt := os.Getenv("XDG_RUNTIME_DIR")
	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(rt, "umbral", "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := client.NewStream(c)
	t.Cleanup(func() { _ = s.Close() })
	for {
		var list struct{ Items []struct{ Health string } }
		if err := s.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) == 1 && list.Items[0].Health == "ok" {
			return s
		}
		select {
		case <-ctx.Done():
			t.Fatal("the model was never discovered after the restart")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
