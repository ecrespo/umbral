package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestTreeFlagsBecomeTheMethodsParameters_REQ_CLI_005 pins the grammar Tech Design §9.4 gives
// the tree commands: each flag and each positional reaches the method's parameters exactly as
// written there, and nothing else does. REQ-CLI-005's "invoke the JSON-RPC method of the same
// name" is only as true as the parameters it sends, and the round trip cannot see a stray
// argument when both sides of its comparison carry it.
func TestTreeFlagsBecomeTheMethodsParameters_REQ_CLI_005(t *testing.T) {
	t.Parallel()

	here, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	ok := func(map[string]any) (any, string) { return map[string]any{}, "" }

	for _, tc := range []struct {
		name   string
		args   []string
		method string
		want   map[string]any
	}{
		{
			name:   "a command after -- is the pane's command, verbatim",
			args:   []string{"pane", "split", "w1:p1", "--", "sh", "-c", "echo --json"},
			method: "pane.split",
			want: map[string]any{
				"pane_id": "w1:p1", "direction": "right",
				"command": []any{"sh", "-c", "echo --json"},
			},
		},
		{
			name:   "flags before the positional, and every split flag",
			args:   []string{"pane", "split", "--direction", "down", "--ratio", "0.3", "--no-focus", "--cwd", "sub", "w1:p1"},
			method: "pane.split",
			want: map[string]any{
				"pane_id": "w1:p1", "direction": "down", "ratio": 0.3, "focus": false,
				"cwd": filepath.Join(here, "sub"),
			},
		},
		{
			name:   "a ratio of zero is sent, for the daemon to reject",
			args:   []string{"pane", "split", "w1:p1", "--ratio", "0"},
			method: "pane.split",
			want:   map[string]any{"pane_id": "w1:p1", "direction": "right", "ratio": 0.0},
		},
		{
			name:   "a relative workspace directory is made absolute here",
			args:   []string{"workspace", "create", "repo", "--label", "api", "--tab-label", "main"},
			method: "workspace.create",
			want: map[string]any{
				"cwd": filepath.Join(here, "repo"), "label": "api", "tab_label": "main",
			},
		},
		{
			name:   "no directory is this one",
			args:   []string{"workspace", "create"},
			method: "workspace.create",
			want:   map[string]any{"cwd": here},
		},
		{
			name:   "rename takes the label positionally",
			args:   []string{"tab", "rename", "w1:t2", "build logs"},
			method: "tab.rename",
			want:   map[string]any{"tab_id": "w1:t2", "label": "build logs"},
		},
		{
			name:   "export without a tab asks for the focused one",
			args:   []string{"layout", "export"},
			method: "layout.export",
			want:   map[string]any{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := serveRecordingDaemon(t, map[string]fakeReply{tc.method: ok})

			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, tc.args[:2]...), "--socket", d.socket, "--no-autostart")
			args = append(args, tc.args[2:]...)
			requireExit(t, run(args, &stdout, &stderr), exitOK, &stderr)

			calls := d.callsExceptHello()
			if len(calls) != 1 || calls[0].Method != tc.method {
				t.Fatalf("the daemon received %v, want exactly one %s", methodsOf(calls), tc.method)
			}
			if got := calls[0].Params; !reflect.DeepEqual(got, normaliseMap(t, tc.want)) {
				t.Errorf("params = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTreeCommandsRejectWhatTheyCannotSend_REQ_CLI_005 is the other half: input the grammar
// does not allow is exit 1 (REQ-CLI-004) and never reaches the daemon.
func TestTreeCommandsRejectWhatTheyCannotSend_REQ_CLI_005(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"a command where none is taken", []string{"workspace", "rename", "w1", "api", "--", "rm", "-rf"}},
		{"a missing positional", []string{"pane", "split"}},
		{"one positional too many", []string{"pane", "get", "w1:p1", "w1:p2"}},
		{"apply without --from", []string{"layout", "apply", "w1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := serveRecordingDaemon(t, nil)

			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, tc.args[:2]...), "--socket", d.socket, "--no-autostart")
			args = append(args, tc.args[2:]...)
			if code := run(args, &stdout, &stderr); code != exitFailure {
				t.Errorf("exit = %d, want %d; stderr: %s", code, exitFailure, stderr.String())
			}
			if calls := d.calls(); len(calls) != 0 {
				t.Errorf("the daemon received %v, want nothing", methodsOf(calls))
			}
		})
	}
}

// TestLayoutApplyReadsEitherFormOfTheTree_REQ_CLI_006: `--from` takes what
// `layout export --json` writes — a whole Layout, of which only `root` is portable — or that
// root on its own; a file that is neither is refused before anything is sent.
func TestLayoutApplyReadsEitherFormOfTheTree_REQ_CLI_006(t *testing.T) {
	t.Parallel()

	node := exportedLayout()["root"]
	for _, tc := range []struct {
		name     string
		document any
		wantRoot any
	}{
		{"a whole layout", exportedLayout(), node},
		{"a bare node", node, node},
		{"neither", map[string]any{"panes": []any{}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := serveRecordingDaemon(t, layoutReplies())

			file := filepath.Join(t.TempDir(), "l.json")
			raw, err := json.Marshal(tc.document)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			var stdout, stderr bytes.Buffer
			code := run([]string{
				"layout", "apply", "w2", "--from", file, "--json",
				"--socket", d.socket, "--no-autostart",
			}, &stdout, &stderr)

			applies := d.callsTo("layout.apply")
			if tc.wantRoot == nil {
				if code != exitFailure || len(applies) != 0 {
					t.Errorf("exit = %d and %d layout.apply calls, want exit 1 and none", code, len(applies))
				}
				return
			}
			requireExit(t, code, exitOK, &stderr)
			if len(applies) != 1 || !reflect.DeepEqual(applies[0].Params["root"], normalise(t, tc.wantRoot)) {
				t.Errorf("layout.apply calls = %v, want one carrying the tree", applies)
			}
		})
	}
}

func normaliseMap(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	out, _ := normalise(t, m).(map[string]any)
	return out
}
