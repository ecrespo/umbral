package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// TestThreadsResumeAfterRestart_REQ_AGT_017 stops the daemon cleanly while one thread's model
// is still streaming and another's finished turn has not been viewed, and starts it again on
// the same database (T-F1-24). Every thread comes back with its whole history: the interrupted
// turn is `stopped`, the unviewed one is still `done` (REQ-AGT-016 survives the restart), and
// the stopped thread takes thread.send with everything it said before. A client focusing the
// `done` thread then makes it `idle`, announced as thread.attention_changed.
func TestThreadsResumeAfterRestart_REQ_AGT_017(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		// The thread nobody views: one finished turn.
		{`{"model":"m","message":{"role":"assistant","content":"Finished."},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":2}`},
		// The busy thread: half an answer, then the daemon stops.
		{`{"model":"m","message":{"role":"assistant","content":"Halfway there, "},"done":false}`, scriptedHold},
		// The busy thread, resumed after the restart.
		{`{"model":"m","message":{"role":"assistant","content":"Resumed."},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`},
	}}
	srv := ollama.server(t)
	stream, ctx, stop := startAgentDaemon(t, srv.URL, daemonOpts{})
	bin := buildDaemon(t)

	create := func(s *client.Stream) string {
		t.Helper()
		var th struct{ ID string }
		if err := s.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &th); err != nil {
			t.Fatal(err)
		}
		return th.ID
	}
	send := func(s *client.Stream, id, text string) {
		t.Helper()
		if err := s.Call(ctx, "thread.send", map[string]any{"thread_id": id, "text": text}, nil); err != nil {
			t.Fatal(err)
		}
	}

	unseen := create(stream)
	send(stream, unseen, "say something")
	crashUntil(ctx, t, stream, func(method string) bool { return method == "thread.turn_finished" })
	busy := create(stream)
	send(stream, busy, "start the work")
	if seen := crashUntil(ctx, t, stream, func(string) bool { return false }); !strings.Contains(seen, "Halfway there, ") {
		t.Fatalf("the client saw %q", seen)
	}
	idle := create(stream)

	stop(os.Interrupt)
	startDaemonLoggingTo(t, bin, os.Getenv("XDG_RUNTIME_DIR"), os.Stderr)
	stream = reconnectAgent(ctx, t)

	var list struct {
		Items []struct {
			ID, State      string
			AttentionState string `json:"attention_state"`
		}
	}
	if err := stream.Call(ctx, "thread.list", nil, &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, th := range list.Items {
		got[th.ID] = th.State + "/" + th.AttentionState
	}
	for id, want := range map[string]string{busy: "stopped/idle", unseen: "idle/done", idle: "idle/idle"} {
		if got[id] != want {
			t.Errorf("after the restart %s is %q, want %q", id, got[id], want)
		}
	}
	b := crashThread(ctx, t, stream, busy)
	if !b.has("user", "start the work") || !b.has("assistant", "Halfway there, ") {
		t.Fatalf("the interrupted thread lost its history: %+v", b.Messages)
	}
	if u := crashThread(ctx, t, stream, unseen); !u.has("assistant", "Finished.") {
		t.Fatalf("the finished thread lost its history: %+v", u.Messages)
	}

	// The stopped thread resumes with its whole history.
	send(stream, busy, "carry on")
	crashUntil(ctx, t, stream, func(method string) bool { return method == "thread.turn_finished" })
	if b := crashThread(ctx, t, stream, busy); b.State != "idle" || !b.has("assistant", "Resumed.") {
		t.Fatalf("the resumed turn: state %q, messages %+v", b.State, b.Messages)
	}
	ollama.mu.Lock()
	last := ollama.bodies[len(ollama.bodies)-1]
	ollama.mu.Unlock()
	for _, want := range []string{"start the work", "Halfway there, ", "carry on"} {
		if !strings.Contains(last, want) {
			t.Errorf("the resumed turn's request lost %q from the history", want)
		}
	}

	// Focusing the thread nobody viewed makes it idle, and says so.
	if err := stream.Call(ctx, "thread.focus", map[string]any{"thread_id": unseen}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method != "thread.attention_changed" {
				continue
			}
			var p struct {
				ThreadID       string `json:"thread_id"`
				AttentionState string `json:"attention_state"`
			}
			if err := n.Decode(&p); err != nil {
				t.Fatal(err)
			}
			if p.ThreadID == unseen && p.AttentionState == "idle" {
				var th struct {
					AttentionState string `json:"attention_state"`
				}
				if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": unseen}, &th); err != nil {
					t.Fatal(err)
				}
				if th.AttentionState != "idle" {
					t.Fatalf("announced idle, stored %q", th.AttentionState)
				}
				return
			}
		case <-deadline:
			t.Fatal("no thread.attention_changed after focusing the done thread")
		}
	}
}
