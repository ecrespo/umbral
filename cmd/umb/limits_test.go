package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// resultTooLarge is what a daemon answers when a result would not fit the frame limit
// (API Spec §3): 5.3 MiB against 4 MiB.
var resultTooLarge = fakeError{
	"code": -32014, "message": "the result is 5557452 bytes, over this connection's 4194304 byte frame limit",
	"data": map[string]any{
		"domain_code": "RESULT_TOO_LARGE", "size_bytes": 5557452, "limit_bytes": 4194304,
	},
}

// TestAnOversizedAnswerTellsTheUserHowToRaiseTheLimit_REQ_CLI_007: a user who hits the limit
// is told the size, the limit and the command that raises it, and `umb` exits 1 — from any
// command, not only the one this test happened to try first.
func TestAnOversizedAnswerTellsTheUserHowToRaiseTheLimit_REQ_CLI_007(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		method string
		args   []string
	}{
		{"umb block last", "block.get", []string{"block", "last"}},
		{"umb status", "system.status", []string{"status"}},
		{"umb workspace list", "workspace.list", []string{"workspace", "list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			socket := serveFakeDaemon(t, map[string]any{tc.method: resultTooLarge})
			var stdout, stderr bytes.Buffer
			code := run(append(tc.args, "--socket", socket, "--no-autostart"), &stdout, &stderr)
			if code != exitFailure {
				t.Errorf("exit = %d, want %d", code, exitFailure)
			}
			want := "umb: the answer is larger than the 4.0 MiB frame limit (5.3 MiB).\n" +
				"     Raise it with: umb limits set --max-message 8MiB\n"
			if stderr.String() != want {
				t.Errorf("stderr =\n%s\nwant\n%s", stderr.String(), want)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}

	t.Run("past the ceiling there is nothing to raise it to", func(t *testing.T) {
		t.Parallel()

		huge := fakeError{"code": -32014, "message": "too large", "data": map[string]any{
			"domain_code": "RESULT_TOO_LARGE", "size_bytes": 70 << 20, "limit_bytes": 64 << 20,
		}}
		socket := serveFakeDaemon(t, map[string]any{"block.get": huge})
		var stdout, stderr bytes.Buffer
		if code := run([]string{"block", "last", "--socket", socket, "--no-autostart"},
			&stdout, &stderr); code != exitFailure {
			t.Errorf("exit = %d, want %d", code, exitFailure)
		}
		if !strings.Contains(stderr.String(), "64.0 MiB frame limit (70.0 MiB)") ||
			strings.Contains(stderr.String(), "Raise it with") {
			t.Errorf("stderr = %q, want the sizes and no hint to raise past the 64 MiB ceiling",
				stderr.String())
		}
	})
}

// frames is a `frames` object as the daemon reports it (API Spec §5.2).
var frames = map[string]any{
	"limit_bytes": 4194304, "largest_in_bytes": 12288, "largest_out_bytes": 3250000,
	"refused_in": 0, "refused_out": 2, "near_limit_out": 5,
}

// framesLine is the delta's `umb status` line for those counters.
const framesLine = "frames: limit 4.0 MiB · largest in 12 KiB, out 3.1 MiB · refused 0 in, 2 out · near limit 5\n"

// TestStatusShowsTheFramesLine_REQ_OBS_005: the counters reach the user without a flag.
func TestStatusShowsTheFramesLine_REQ_OBS_005(t *testing.T) {
	t.Parallel()

	socket := serveFakeDaemon(t, map[string]any{"system.status": map[string]any{
		"daemon_version": "0.1.0", "uptime_ms": 1000, "sessions_alive": 1, "threads_running": 0,
		"providers": []any{}, "mcp": []any{}, "frames": frames,
	}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasSuffix(stdout.String(), framesLine) {
		t.Errorf("stdout =\n%s\nwant it to end with\n%s", stdout.String(), framesLine)
	}
}

// TestLimitsShowsTheLimitAndTheCounters_REQ_OBS_005 is `umb limits`, human and --json.
func TestLimitsShowsTheLimitAndTheCounters_REQ_OBS_005(t *testing.T) {
	t.Parallel()

	answer := map[string]any{"frames": frames, "configured_max_message_bytes": 4194304}
	socket := serveFakeDaemon(t, map[string]any{"limits.get": answer})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"limits", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	want := "frame limit: 4.0 MiB for new connections\n" + framesLine
	if stdout.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	stdout.Reset()
	if code := run([]string{"limits", "--json", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--json exit = %d, stderr = %q", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got["configured_max_message_bytes"] != float64(4194304) {
		t.Errorf("--json printed %q (%v)", stdout.String(), err)
	}
}

// TestLimitsSetSendsTheSizeInBytes_REQ_CLI_007: `umb limits set --max-message 8MiB` asks the
// daemon for 8 388 608 bytes, and a size `umb` cannot read never reaches the daemon.
func TestLimitsSetSendsTheSizeInBytes_REQ_CLI_007(t *testing.T) {
	t.Parallel()

	seen := make(chan map[string]any, 4)
	socket := serveFakeDaemonWithParams(t, map[string]any{"limits.set": map[string]any{
		"max_message_bytes": 8388608, "applies_to": "new_connections",
	}}, seen)

	var stdout, stderr bytes.Buffer
	code := run([]string{"limits", "set", "--max-message", "8MiB", "--socket", socket, "--no-autostart"},
		&stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if params := <-seen; params["max_message_bytes"] != float64(8388608) {
		t.Errorf("limits.set params = %v, want max_message_bytes 8388608", params)
	}
	if want := "frame limit set to 8.0 MiB; it applies to new connections\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}

	for _, bad := range [][]string{{"--max-message", "lots"}, {}} {
		stdout.Reset()
		stderr.Reset()
		args := append(append([]string{"limits", "set"}, bad...), "--socket", socket, "--no-autostart")
		if code := run(args, &stdout, &stderr); code != exitFailure {
			t.Errorf("%v: exit = %d, want %d", bad, code, exitFailure)
		}
		if stderr.Len() == 0 {
			t.Errorf("%v: nothing on stderr", bad)
		}
		select {
		case params := <-seen:
			t.Errorf("%v reached the daemon with %v", bad, params)
		default:
		}
	}
}

// TestFormatSize pins the renderings the delta prints.
func TestFormatSize(t *testing.T) {
	t.Parallel()

	for n, want := range map[int64]string{
		512: "512 B", 12288: "12 KiB", 4194304: "4.0 MiB", 3250000: "3.1 MiB", 5557452: "5.3 MiB",
	} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestStatusShowsWhyAProviderIsDown_REQ_SEC_008: the reason reaches the user, not only the
// wire (REQ-SEC-008 "show that reason in umb status", REQ-SEC-012 for env_secret).
func TestStatusShowsWhyAProviderIsDown_REQ_SEC_008(t *testing.T) {
	t.Parallel()

	socket := serveFakeDaemon(t, map[string]any{"system.status": map[string]any{
		"daemon_version": "0.1.0", "uptime_ms": 1000, "sessions_alive": 0, "threads_running": 0,
		"providers": []any{
			map[string]any{"id": "hf", "health": "down", "reason": "keyring_unavailable"},
			map[string]any{"id": "openrouter", "health": "degraded", "reason": "env_secret"},
			map[string]any{"id": "ollama", "health": "unknown"},
		},
		"mcp": []any{},
	}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	want := "providers: hf (down: keyring_unavailable), openrouter (degraded: env_secret), ollama (unknown)\n"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout =\n%s\nwant a line\n%s", stdout.String(), want)
	}
}
