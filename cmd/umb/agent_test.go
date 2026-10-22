package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestUmbAgentExplain_REQ_XAG_003: `umb agent explain PANE` sends the pane, prints the agent, the
// manifest and rule that matched, who decides the pane's state and every skipped manifest, with
// control characters escaped; `--json` prints the daemon's answer as it came.
func TestUmbAgentExplain_REQ_XAG_003(t *testing.T) {
	t.Parallel()
	explained := map[string]any{
		"pane_id":         "w1:p2",
		"added_later":     "kept",
		"detected_agent":  map[string]any{"name": "opencode", "state_source": "acp"},
		"manifest":        map[string]any{"name": "opencode", "display_name": "OpenCode", "origin": "builtin"},
		"rule":            map[string]any{"kind": "executable", "pattern": "opencode", "pid": 4821, "executable": "opencode"},
		"state_authority": map[string]any{"attention_state": "idle", "state_source": "umbral:shell"},
		"skipped_manifests": []any{
			map[string]any{"file": "/c/agents/aider.toml", "reason": "invalid agent manifest: \x1b[31mbad"},
		},
	}
	seen := make(chan map[string]any, 4)
	socket := serveFakeDaemonWithParams(t, map[string]any{"agent.explain": explained}, seen)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"agent", "explain", "w1:p2", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	var got map[string]any
	for p := range seen {
		if _, ok := p["pane_id"]; ok {
			got = p
			break
		}
	}
	if got["pane_id"] != "w1:p2" || len(got) != 1 {
		t.Errorf("params = %v, want {pane_id: w1:p2}", got)
	}
	want := "pane      w1:p2\n" +
		"agent     opencode (OpenCode), could report through acp\n" +
		"manifest  builtin\n" +
		"rule      executable \"opencode\" matched pid 4821 (opencode)\n" +
		"state     idle, decided by umbral:shell\n" +
		"skipped   /c/agents/aider.toml: invalid agent manifest: \\x1b[31mbad\n"
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	stdout.Reset()
	if code := run([]string{"agent", "explain", "--json", "w1:p2", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--json exit = %d, stderr %q", code, stderr.String())
	}
	var asJSON map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &asJSON); err != nil || asJSON["pane_id"] != "w1:p2" || asJSON["added_later"] != "kept" {
		t.Errorf("--json printed %q (%v)", stdout.String(), err)
	}

	none := serveFakeDaemon(t, map[string]any{"agent.explain": map[string]any{
		"pane_id": "w1:p3", "detected_agent": nil, "manifest": nil, "rule": nil,
		"state_authority":   map[string]any{"attention_state": "unknown", "state_source": nil},
		"skipped_manifests": []any{},
	}})
	stdout.Reset()
	if code := run([]string{"agent", "explain", "w1:p3", "--socket", none, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("none: exit = %d, stderr %q", code, stderr.String())
	}
	if want := "pane      w1:p3\nagent     none detected\nstate     unknown, decided by no source\n"; stdout.String() != want {
		t.Errorf("none: stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	stderr.Reset()
	if code := run([]string{"agent", "explain", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitFailure {
		t.Errorf("no pane: exit = %d", code)
	}
	if code := run([]string{"agent", "list"}, &stdout, &stderr); code != exitFailure {
		t.Errorf("unknown subcommand: exit = %d", code)
	}
}
