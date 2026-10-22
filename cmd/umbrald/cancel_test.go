package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// running reports whether a process whose command line contains marker is alive.
func running(t *testing.T, marker string) bool {
	t.Helper()
	err := exec.CommandContext(t.Context(), "pgrep", "-f", marker).Run()
	if err == nil {
		return true
	}
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		return false
	}
	t.Fatalf("pgrep: %v", err)
	return false
}

// TestCancelUnder500ms_REQ_AGT_007: through a real daemon, the agent runs `sleep 60` in the
// thread's PTY; thread.cancel answers within 500 ms, by which time the sleep is gone, the turn
// has finished `cancelled` and the thread is `stopped`.
func TestCancelUnder500ms_REQ_AGT_007(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skipf("pgrep is not installed: %v", err)
	}
	// A duration no other process on the machine is running.
	seconds := fmt.Sprintf("60.%d", os.Getpid())
	command := "sleep " + seconds
	ollama := &scriptedOllama{replies: [][]string{
		{fmt.Sprintf(`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"run_command","arguments":{"command":%q}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`, command)},
	}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)

	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "wait a minute"}, nil); err != nil {
		t.Fatal(err)
	}
	// run_command asks in normal mode: approve it, then wait for the sleep to start.
	for approved := false; !approved; {
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
				approved = true
			}
		case <-ctx.Done():
			t.Fatal("run_command never asked")
		}
	}
	for !running(t, command) {
		select {
		case <-ctx.Done():
			t.Fatal("the sleep never started")
		case <-time.After(20 * time.Millisecond):
		}
	}

	start := time.Now()
	var res struct {
		StoppedAt *int64 `json:"stopped_at"`
	}
	if err := stream.Call(ctx, "thread.cancel", map[string]any{"thread_id": thread.ID}, &res); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	stillRunning := running(t, command)
	if took >= 500*time.Millisecond || stillRunning || res.StoppedAt == nil {
		t.Fatalf("thread.cancel answered in %v, stopped_at %v, the sleep still running: %v", took, res.StoppedAt, stillRunning)
	}
	t.Logf("thread.cancel answered in %v", took)
	// The answer comes once the turn has recorded its end.
	var after struct{ State string }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &after); err != nil || after.State != "stopped" {
		t.Fatalf("right after thread.cancel the thread is %q (%v), want stopped", after.State, err)
	}

	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method != "thread.turn_finished" {
				continue
			}
			var end struct {
				StopReason string `json:"stop_reason"`
			}
			_ = n.Decode(&end)
			var got struct{ State string }
			if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": thread.ID}, &got); err != nil {
				t.Fatal(err)
			}
			if end.StopReason != "cancelled" || got.State != "stopped" {
				t.Fatalf("turn finished %s, thread %s; want cancelled and stopped", end.StopReason, got.State)
			}
			return
		case <-ctx.Done():
			t.Fatal("no turn_finished after the cancel")
		}
	}
}
