package main

import (
	"bytes"
	"testing"
)

// TestUmbNotifyAsksTheDaemon_REQ_NTF_001: a hook raises a notification with `umb notify`,
// which sends API §5.35's params as typed and prints the typed reason. A notification that
// was not shown is still an answer, so `umb` exits 0.
func TestUmbNotifyAsksTheDaemon_REQ_NTF_001(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		args   []string
		result map[string]any
		want   map[string]any
		out    string
	}{
		{
			"every member",
			[]string{"notify", "build done", "--source", "ci-hook", "--body", "all green", "--sound", "done"},
			map[string]any{"shown": true, "reason": "shown"},
			map[string]any{"title": "build done", "source": "ci-hook", "body": "all green", "sound": "done"},
			"shown\n",
		},
		{
			"title and source only",
			[]string{"notify", "--source", "ci-hook", "build", "done"},
			map[string]any{"shown": false, "reason": "rate_limited"},
			map[string]any{"title": "build done", "source": "ci-hook"},
			"not shown: rate_limited\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seen := make(chan map[string]any, 4)
			socket := serveFakeDaemonWithParams(t, map[string]any{"notification.show": tc.result}, seen)
			var stdout, stderr bytes.Buffer
			if code := run(append(tc.args, "--socket", socket, "--no-autostart"), &stdout, &stderr); code != exitOK {
				t.Fatalf("exit = %d, stderr %q", code, stderr.String())
			}
			var got map[string]any
			for p := range seen {
				if _, ok := p["title"]; ok {
					got = p
					break
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("params = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("params[%s] = %v, want %v", k, got[k], v)
				}
			}
			if stdout.String() != tc.out {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.out)
			}
		})
	}
}

// TestUmbNotifyNeedsATitleAndASource: both are required by §5.35, so `umb` says so before it
// connects.
func TestUmbNotifyNeedsATitleAndASource(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"notify", "--source", "x"}, {"notify", "title"}} {
		var stdout, stderr bytes.Buffer
		if code := run(append(args, "--socket", "/nonexistent/umbral.sock", "--no-autostart"), &stdout, &stderr); code != exitFailure {
			t.Errorf("%v: exit = %d, want %d", args, code, exitFailure)
		}
		if stderr.Len() == 0 {
			t.Errorf("%v: nothing on stderr", args)
		}
	}
}
