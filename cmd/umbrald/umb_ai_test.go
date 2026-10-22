package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildUmb builds the umb binary for a test that drives it as a user would.
func buildUmb(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "umb")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/umb")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build umb: %v\n%s", err, out)
	}
	return bin
}

// TestUmbAiAgainstARealDaemon_REQ_CLI_001 pipes a test's output into the real `umb ai`
// against a real daemon: the model (an Ollama served locally) reads what was piped, its
// answer streams to stdout, the thread is ephemeral — thread.list does not show it — and a
// turn the provider fails exits 1.
func TestUmbAiAgainstARealDaemon_REQ_CLI_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{
			`{"model":"m","message":{"role":"assistant","content":"x_test.go expects "},"done":false}`,
			`{"model":"m","message":{"role":"assistant","content":"2, not 1."},"done":true,"done_reason":"stop","prompt_eval_count":30,"eval_count":6}`,
		},
		// The second run's turn gets an empty stream: provider_error.
		{},
	}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)
	umb := buildUmb(t)
	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "umbral", "umbral.sock")
	dir := t.TempDir()

	ai := func(stdin string, args ...string) (int, string, string) {
		cmd := exec.CommandContext(ctx, umb, append([]string{"ai", "--socket", socket, "--no-autostart"}, args...)...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), stdout.String(), stderr.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, stdout.String(), stderr.String()
	}

	piped := "--- FAIL: TestSum (0.00s)\n    x_test.go:9: got 1, want 2\n"
	code, stdout, stderr := ai(piped, "why does it fail?")
	if code != 0 || stdout != "x_test.go expects 2, not 1.\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	ollama.mu.Lock()
	first := ollama.bodies[0]
	ollama.mu.Unlock()
	if !strings.Contains(first, "got 1, want 2") || !strings.Contains(first, "why does it fail?") {
		t.Fatalf("the model never read stdin and the prompt: %s", first)
	}
	// ask mode exposes read-only tools only (Tech §5.3): nothing that writes, runs or fetches.
	for _, tool := range []string{"write_file", "edit_file", "run_command", "fetch_url"} {
		if strings.Contains(first, `"`+tool+`"`) {
			t.Fatalf("an ask thread offered %s: %s", tool, first)
		}
	}
	if !strings.Contains(first, `"read_file"`) {
		t.Fatalf("an ask thread offers the read-only tools: %s", first)
	}

	var list struct{ Items []struct{ ID string } }
	if err := stream.Call(ctx, "thread.list", map[string]any{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("thread.list shows the ephemeral thread: %+v", list.Items)
	}

	code, _, stderr = ai("", "and now?")
	if code != 1 || !strings.Contains(stderr, "provider_error") {
		t.Fatalf("a failed turn: exit %d, stderr %q", code, stderr)
	}
}
