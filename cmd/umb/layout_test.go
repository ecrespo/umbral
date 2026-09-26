package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// exportedLayout is what the fake daemon answers to `layout.export`: API Spec §4's Layout,
// with a split, a label, an env and a command, so the round trip has every field REQ-WS-004
// puts in the portable tree to lose.
func exportedLayout() map[string]any {
	return map[string]any{
		"workspace_id": "w1", "tab_id": "w1:t1", "focused_pane_id": "w1:p1",
		"root": map[string]any{
			"type": "split", "direction": "right", "ratio": 0.6,
			"first": map[string]any{"type": "pane", "pane_id": "w1:p1", "label": "editor", "cwd": "/repo"},
			"second": map[string]any{
				"type": "pane", "pane_id": "w1:p2", "label": "tests", "cwd": "/repo",
				"command": []any{"sh", "-c", "go test ./..."},
				"env":     map[string]any{"UMBRAL_ROLE": "tests"},
			},
		},
	}
}

// The two warnings of API Spec §5.8, the second one present because the tree carries a
// command.
var applyWarnings = []any{
	"live processes and scrollback are not reproduced",
	"commands are pending: they are typed at each pane's prompt and run when you press Enter",
}

// applyResult is what the fake daemon answers to `layout.apply` into w2.
func applyResult() map[string]any {
	pane := func(id, label string) map[string]any {
		return map[string]any{
			"id": id, "tab_id": "w2:t1", "workspace_id": "w2",
			"session_id": "ses_01J9Z3K8T2QH6W4V5X7Y8Z9A0E", "thread_id": nil,
			"label": label, "cwd": "/repo", "aliases": []any{id},
			"attention_state": "unknown", "state_source": nil, "metadata": map[string]any{},
			"created_at": 1757592000000, "closed_at": nil,
		}
	}
	tests := pane("w2:p2", "tests")
	tests["command"] = []any{"sh", "-c", "go test ./..."}
	tests["env"] = map[string]any{"UMBRAL_ROLE": "tests"}
	tests["command_pending"] = true
	return map[string]any{
		"tab": map[string]any{
			"id": "w2:t1", "workspace_id": "w2", "label": "", "order_index": 0,
			"focused_pane_id": "w2:p1", "created_at": 1757592000000, "closed_at": nil,
		},
		"panes":    []any{pane("w2:p1", "editor"), tests},
		"warnings": applyWarnings,
	}
}

func layoutReplies() map[string]fakeReply {
	return map[string]fakeReply{
		"layout.export": func(map[string]any) (any, string) { return exportedLayout(), "" },
		"layout.apply":  func(map[string]any) (any, string) { return applyResult(), "" },
	}
}

// TestLayoutExportApplyThroughAPipe_REQ_CLI_006 is REQ-CLI-006 as the shell sees it: "WHEN
// `umb layout export --json` runs, THE SYSTEM SHALL write the layout to stdout in the form
// `umb layout apply` consumes, and WHEN `umb layout apply --from <file>` runs, THE SYSTEM
// SHALL read the layout from that file, or from stdin WHERE the file is `-`, so that a script
// reproduces a tab in another workspace through a pipe".
//
// It runs two real `umb` processes joined by an OS pipe —
// `umb layout export w1:t1 --json | umb layout apply w2 --from -` — rather than calling
// `run` twice, because the requirement is about the pipe: stdin is the thing under test, and
// an in-process call has no stdin of its own to give. The daemon is a fake, as in every
// other `cmd/umb` test; reproducing the tab is the daemon's work (REQ-WS-005), and what this
// test can hold the CLI to is that the tree `layout.apply` receives is the tree
// `layout.export` returned, untouched, aimed at the workspace named on the command line.
// The end-to-end round trip against a real daemon is `scripts/cli_roundtrip.sh`.
func TestLayoutExportApplyThroughAPipe_REQ_CLI_006(t *testing.T) {
	t.Parallel()

	umb := buildUmb(t)
	d := serveRecordingDaemon(t, layoutReplies())
	env := isolatedEnv(t)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	var exportErr, applyOut, applyErr bytes.Buffer
	export := exec.CommandContext(t.Context(), umb,
		"layout", "export", "w1:t1", "--json", "--socket", d.socket, "--no-autostart")
	export.Env, export.Stdout, export.Stderr = env, pw, &exportErr

	apply := exec.CommandContext(t.Context(), umb,
		"layout", "apply", "w2", "--from", "-", "--socket", d.socket, "--no-autostart")
	apply.Env, apply.Stdin, apply.Stdout, apply.Stderr = env, pr, &applyOut, &applyErr

	if err := export.Start(); err != nil {
		t.Fatalf("start umb layout export: %v", err)
	}
	if err := apply.Start(); err != nil {
		t.Fatalf("start umb layout apply: %v", err)
	}
	// The children hold their own copies; closing the parent's is what lets apply see EOF
	// once export exits.
	_ = pw.Close()
	_ = pr.Close()

	exportCode, applyCode := exitCodeOf(t, export.Wait()), exitCodeOf(t, apply.Wait())
	if exportCode != exitOK {
		t.Fatalf("`umb layout export w1:t1 --json` exit = %d, want 0; stderr: %s", exportCode, exportErr.String())
	}
	if applyCode != exitOK {
		t.Fatalf("`umb layout apply w2 --from -` exit = %d, want 0; stderr: %s", applyCode, applyErr.String())
	}

	exports := d.callsTo("layout.export")
	if len(exports) != 1 || exports[0].Params["tab_id"] != "w1:t1" {
		t.Fatalf("layout.export calls = %v, want one with tab_id \"w1:t1\"", exports)
	}
	applies := d.callsTo("layout.apply")
	if len(applies) != 1 {
		t.Fatalf("layout.apply was called %d times, want exactly 1", len(applies))
	}
	params := applies[0].Params
	if params["workspace_id"] != "w2" {
		t.Errorf("layout.apply workspace_id = %v, want the positional \"w2\"", params["workspace_id"])
	}
	if want := exportedLayout()["root"]; !reflect.DeepEqual(params["root"], normalise(t, want)) {
		t.Errorf("layout.apply root =\n  %v\nwant the exported tree unchanged\n  %v\nthe second tab would not reproduce the first", params["root"], want)
	}
	if !strings.Contains(applyOut.String(), "w2:t1") {
		t.Errorf("apply stdout = %q does not name the tab it created (w2:t1)", applyOut.String())
	}
}

// TestLayoutApplyReportsWarnings_REQ_CLI_006 covers the file branch of REQ-CLI-006 ("read the
// layout from that file") and what the delta's Verification asks of it: REQ-WS-005's
// warnings — "SHALL state in the response that live processes and scrollback are not
// reproduced" — "reach the user's terminal rather than being dropped by the printer".
//
// The warnings are the only place the user learns that the commands in the new tab were
// typed and not run (REQ-TERM-011). A human printer that shows the tab and the panes and
// leaves the warnings out turns a deliberate safety property into a tab that looks broken,
// so both output shapes are checked, and the warnings must not turn success into exit 1.
func TestLayoutApplyReportsWarnings_REQ_CLI_006(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "l.json")
	raw, err := json.Marshal(exportedLayout())
	if err != nil {
		t.Fatalf("marshal the layout: %v", err)
	}
	if err := os.WriteFile(file, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}

	t.Run("human", func(t *testing.T) {
		t.Parallel()
		d := serveRecordingDaemon(t, layoutReplies())

		var stdout, stderr bytes.Buffer
		code := run([]string{
			"layout", "apply", "w2", "--from", file,
			"--socket", d.socket, "--no-autostart",
		}, &stdout, &stderr)
		requireExit(t, code, exitOK, &stderr)
		requireAppliedFromFile(t, d)

		terminal := stdout.String() + stderr.String()
		for _, w := range applyWarnings {
			if !strings.Contains(terminal, w.(string)) {
				t.Errorf("the terminal never shows %q (stdout %q, stderr %q); the user is not told the tab is not a live copy", w, stdout.String(), stderr.String())
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()
		d := serveRecordingDaemon(t, layoutReplies())

		var stdout, stderr bytes.Buffer
		code := run([]string{
			"layout", "apply", "w2", "--from", file, "--json",
			"--socket", d.socket, "--no-autostart",
		}, &stdout, &stderr)
		requireExit(t, code, exitOK, &stderr)
		requireAppliedFromFile(t, d)

		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v (%q)", err, stdout.String())
		}
		if !reflect.DeepEqual(got["warnings"], applyWarnings) {
			t.Errorf("printed warnings = %v, want %v as the daemon sent them", got["warnings"], applyWarnings)
		}
	})
}

// requireAppliedFromFile checks the daemon got the tree from the file, aimed at w2.
func requireAppliedFromFile(t *testing.T, d *recordingDaemon) {
	t.Helper()
	applies := d.callsTo("layout.apply")
	if len(applies) != 1 {
		t.Fatalf("layout.apply was called %d times, want exactly 1", len(applies))
	}
	if applies[0].Params["workspace_id"] != "w2" {
		t.Errorf("layout.apply workspace_id = %v, want the positional \"w2\"", applies[0].Params["workspace_id"])
	}
	if want := normalise(t, exportedLayout()["root"]); !reflect.DeepEqual(applies[0].Params["root"], want) {
		t.Errorf("layout.apply root = %v, want the tree read from --from\n  %v", applies[0].Params["root"], want)
	}
}

// normalise passes v through JSON so it compares with what the fake daemon decoded.
func normalise(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// buildUmb compiles this command into the test's temporary directory, for the tests that
// need what `run` cannot give them: a process with its own stdin.
func buildUmb(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "umb")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build umb: %v\n%s", err, out)
	}
	return bin
}

// isolatedEnv is the environment for a child `umb`. Every command runs with --no-autostart
// against a fake socket, so nothing should be written anywhere; the XDG directories and HOME
// point into the test's temporary directory anyway, so that a regression that autostarts a
// daemon or writes state lands there and not in the developer's own.
func isolatedEnv(t *testing.T) []string {
	t.Helper()

	root := t.TempDir()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "XDG_") || name == "HOME" || name == "UMBRAL_SESSION_ID" {
			continue
		}
		env = append(env, kv)
	}
	for _, name := range []string{"HOME", "XDG_RUNTIME_DIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		dir := filepath.Join(root, strings.ToLower(name))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		env = append(env, name+"="+dir)
	}
	return env
}

// exitCodeOf turns the result of Wait into the process's exit code.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	t.Fatalf("wait: %v", err)
	return -1
}
