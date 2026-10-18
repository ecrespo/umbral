package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestUmbWaitListsOpenWaits_REQ_AUT_006: `umb wait ls` prints each open wait with its target,
// what it waits for, its age and whether its thread is stalled, and `--json` the daemon's
// answer as it came.
func TestUmbWaitListsOpenWaits_REQ_AUT_006(t *testing.T) {
	t.Parallel()
	list := map[string]any{"items": []any{
		map[string]any{
			"wait_id": "wt_1", "connection_id": "con_1", "target": map[string]any{"thread_id": "thr_1"},
			"until": []any{"done", "blocked"}, "regex": nil, "age_ms": 4210, "stalled": true,
		},
		map[string]any{
			"wait_id": "wt_2", "connection_id": "con_2", "target": map[string]any{"block_id": "blk_1"},
			"until": []any{}, "regex": "PASS|FAIL", "age_ms": 900, "stalled": false,
		},
	}}
	socket := serveFakeDaemon(t, map[string]any{"wait.list": list})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"wait", "ls", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	want := "wt_1  thread thr_1  until done,blocked  4.2s  stalled  con_1\n" +
		"wt_2  block blk_1  regex \"PASS|FAIL\"  0.9s  con_2\n"
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	stdout.Reset()
	if code := run([]string{"wait", "ls", "--json", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--json exit = %d, stderr %q", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got["items"].([]any)) != 2 {
		t.Errorf("--json printed %q (%v)", stdout.String(), err)
	}

	empty := serveFakeDaemon(t, map[string]any{"wait.list": map[string]any{"items": []any{}}})
	stdout.Reset()
	if code := run([]string{"wait", "ls", "--socket", empty, "--no-autostart"}, &stdout, &stderr); code != exitOK || stdout.String() != "no open waits\n" {
		t.Errorf("no waits: exit %d, stdout %q", code, stdout.String())
	}
}

// TestUmbWaitCancelEndsOneWait_REQ_AUT_007: `umb wait cancel WAIT_ID` sends the id and says
// it is cancelled; an unknown id is the daemon's NOT_FOUND and exits 1; the id is required.
func TestUmbWaitCancelEndsOneWait_REQ_AUT_007(t *testing.T) {
	t.Parallel()
	seen := make(chan map[string]any, 4)
	socket := serveFakeDaemonWithParams(t, map[string]any{"wait.cancel": map[string]any{}}, seen)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"wait", "cancel", "wt_1", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	var got map[string]any
	for p := range seen {
		if _, ok := p["wait_id"]; ok {
			got = p
			break
		}
	}
	if got["wait_id"] != "wt_1" || len(got) != 1 {
		t.Errorf("params = %v, want {wait_id: wt_1}", got)
	}
	if stdout.String() != "cancelled wt_1\n" {
		t.Errorf("stdout = %q", stdout.String())
	}

	missing := serveFakeDaemon(t, map[string]any{"wait.cancel": fakeError{
		"code": -32002, "message": "no open wait wt_9", "data": map[string]any{"domain_code": "NOT_FOUND"},
	}})
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"wait", "cancel", "wt_9", "--socket", missing, "--no-autostart"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "no open wait wt_9") {
		t.Errorf("unknown id: exit %d, stderr %q", code, stderr.String())
	}

	stderr.Reset()
	if code := run([]string{"wait", "cancel"}, &stdout, &stderr); code != exitFailure || !strings.Contains(stderr.String(), "WAIT_ID") {
		t.Errorf("no id: exit %d, stderr %q", code, stderr.String())
	}
}
