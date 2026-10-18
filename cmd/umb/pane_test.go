package main

import (
	"bytes"
	"testing"
)

// TestPaneReportStateFromAHook_REQ_INT_002: `umb pane report_state` is one call to
// `pane.report_state` with the pane the hook runs in — $UMBRAL_PANE_ID unless one is named —
// sends `seq` only when given, prints nothing on success, and `release_state` gives the pane
// back.
//
// Not parallel: it sets UMBRAL_PANE_ID.
func TestPaneReportStateFromAHook_REQ_INT_002(t *testing.T) {
	ok := func(map[string]any) (any, string) { return map[string]any{}, "" }
	d := serveRecordingDaemon(t, map[string]fakeReply{"pane.report_state": ok, "pane.release_state": ok})
	t.Setenv("UMBRAL_PANE_ID", "w2:p4")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"pane", "report_state", "blocked", "--source", "claude-code", "--agent", "claude",
		"--message", "approve?", "--seq", "0", "--socket", d.socket, "--no-autostart",
	}, &stdout, &stderr)
	requireExit(t, code, exitOK, &stderr)
	if stdout.Len() != 0 {
		t.Errorf("a successful report printed %q", stdout.String())
	}
	code = run([]string{
		"pane", "report_state", "idle", "w1:p1", "--source", "claude-code",
		"--socket", d.socket, "--no-autostart",
	}, &stdout, &stderr)
	requireExit(t, code, exitOK, &stderr)
	code = run([]string{
		"pane", "release_state", "--source", "claude-code", "--socket", d.socket, "--no-autostart",
	}, &stdout, &stderr)
	requireExit(t, code, exitOK, &stderr)

	reports := d.callsTo("pane.report_state")
	if len(reports) != 2 {
		t.Fatalf("pane.report_state was called %d times, want 2", len(reports))
	}
	first, second := reports[0].Params, reports[1].Params
	if first["pane_id"] != "w2:p4" || first["state"] != "blocked" || first["source"] != "claude-code" ||
		first["agent"] != "claude" || first["message"] != "approve?" || first["seq"] != float64(0) {
		t.Errorf("the first report sent %v", first)
	}
	if _, sent := second["seq"]; sent || second["pane_id"] != "w1:p1" {
		t.Errorf("the second report sent %v: the named pane, and no seq", second)
	}
	releases := d.callsTo("pane.release_state")
	if len(releases) != 1 || releases[0].Params["pane_id"] != "w2:p4" || releases[0].Params["source"] != "claude-code" {
		t.Errorf("pane.release_state got %v", releases)
	}
}

// TestPaneReportStateNeedsAPane_REQ_INT_002: outside a pane, with none named, the command
// fails before connecting.
//
// Not parallel: it clears UMBRAL_PANE_ID.
func TestPaneReportStateNeedsAPane_REQ_INT_002(t *testing.T) {
	t.Setenv("UMBRAL_PANE_ID", "")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"pane", "report_state", "idle", "--source", "x", "--socket", "/nonexistent/umbral.sock", "--no-autostart",
	}, &stdout, &stderr)
	if code != exitFailure || !bytes.Contains(stderr.Bytes(), []byte("UMBRAL_PANE_ID")) {
		t.Errorf("exit %d, stderr %q; want 1 naming UMBRAL_PANE_ID", code, stderr.String())
	}
}
