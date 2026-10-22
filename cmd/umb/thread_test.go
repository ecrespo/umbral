package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestThreadCommandsDriveAThread_REQ_CLI_009: a script drives a whole thread through `umb
// thread` — create, send with a wait, wait again, read it, cancel — each command one call to the
// method of the same name, and each printing the one value the next command needs.
func TestThreadCommandsDriveAThread_REQ_CLI_009(t *testing.T) {
	t.Parallel()
	d := serveRecordingDaemon(t, map[string]fakeReply{
		"thread.create": func(map[string]any) (any, string) {
			return map[string]any{"id": "thr_1", "mode": "normal", "attention_state": "idle"}, ""
		},
		"thread.send": func(p map[string]any) (any, string) {
			if p["wait"] == nil {
				return map[string]any{"turn_id": "trn_2", "message_id": "msg_2"}, ""
			}
			return map[string]any{"turn_id": "trn_1", "message_id": "msg_1", "final_state": "done"}, ""
		},
		"thread.wait": func(map[string]any) (any, string) {
			return map[string]any{"thread_id": "thr_1", "turn_id": "trn_2", "state": "blocked", "waited_ms": 12}, ""
		},
		"thread.get": func(map[string]any) (any, string) {
			return map[string]any{"thread": map[string]any{"id": "thr_1", "attention_state": "done"}, "messages": []any{
				map[string]any{"role": "user", "content": "say pong"},
				map[string]any{"role": "assistant", "content": "pong"},
			}}, ""
		},
		"thread.cancel": func(map[string]any) (any, string) { return map[string]any{"stopped_at": nil}, "" },
	})
	sock := []string{"--socket", d.socket, "--no-autostart"}
	umb := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		requireExit(t, run(append(args, sock...), &stdout, &stderr), exitOK, &stderr)
		return stdout.String()
	}

	if got := umb("thread", "create", "/work", "--mode", "plan", "--title", "nightly"); got != "thr_1\n" {
		t.Errorf("thread create printed %q, want the id alone", got)
	}
	if got := umb("thread", "send", "thr_1", "say", "pong", "--wait", "done,blocked", "--timeout", "5m"); got != "trn_1 done\n" {
		t.Errorf("thread send --wait printed %q, want the turn and its final state", got)
	}
	if got := umb("thread", "send", "thr_1", "again"); got != "trn_2\n" {
		t.Errorf("thread send printed %q, want the turn", got)
	}
	if got := umb("thread", "wait", "thr_1", "--until", "done,blocked", "--timeout", "30s"); got != "blocked\n" {
		t.Errorf("thread wait printed %q, want the state alone", got)
	}
	if got := umb("thread", "get", "thr_1"); !strings.Contains(got, "assistant: pong") {
		t.Errorf("thread get printed %q, want each message with its role", got)
	}
	umb("thread", "cancel", "thr_1")

	if got, want := methodsOf(d.callsExceptHello()), []string{
		"thread.create", "thread.send", "thread.send", "thread.wait", "thread.get", "thread.cancel",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	calls := d.callsExceptHello()
	if p := calls[0].Params; p["cwd"] != "/work" || p["mode"] != "plan" || p["title"] != "nightly" {
		t.Errorf("thread.create sent %v", p)
	}
	wantWait := map[string]any{"until": []any{"done", "blocked"}, "timeout_ms": float64(300000)}
	if p := calls[1].Params; p["thread_id"] != "thr_1" || p["text"] != "say pong" || !reflect.DeepEqual(p["wait"], wantWait) {
		t.Errorf("thread.send --wait sent %v", p)
	}
	if _, sent := calls[2].Params["wait"]; sent {
		t.Errorf("thread.send without --wait sent a wait: %v", calls[2].Params)
	}
	if p := calls[3].Params; p["thread_id"] != "thr_1" || !reflect.DeepEqual(p["until"], []any{"done", "blocked"}) || p["timeout_ms"] != float64(30000) {
		t.Errorf("thread.wait sent %v", p)
	}
	if p := calls[4].Params; p["include_messages"] != true {
		t.Errorf("thread.get sent %v, want include_messages", p)
	}
}

// TestThreadWaitTimeoutExitsOne_REQ_CLI_009: a wait that reaches its deadline is the daemon's
// TIMEOUT, so exit 1 (REQ-CLI-004) naming the state it last saw (REQ-AUT-004); a wait without
// --until is refused before anything is sent.
func TestThreadWaitTimeoutExitsOne_REQ_CLI_009(t *testing.T) {
	t.Parallel()
	d := serveRecordingDaemon(t, map[string]fakeReply{
		"thread.wait": func(map[string]any) (any, string) { return map[string]any{"last_state": "working"}, "TIMEOUT" },
	})
	var stdout, stderr bytes.Buffer
	code := run([]string{"thread", "wait", "thr_1", "--until", "done", "--timeout", "1s", "--socket", d.socket, "--no-autostart"}, &stdout, &stderr)
	requireExit(t, code, exitFailure, &stderr)
	if !strings.Contains(stderr.String(), "working") {
		t.Errorf("a timed-out wait said %q, want the last state it observed", stderr.String())
	}
	stderr.Reset()
	code = run([]string{"thread", "wait", "thr_1", "--socket", d.socket, "--no-autostart"}, &stdout, &stderr)
	requireExit(t, code, exitFailure, &stderr)
	if n := len(d.callsTo("thread.wait")); n != 1 {
		t.Errorf("a wait without --until reached the daemon (%d waits sent)", n)
	}
}
