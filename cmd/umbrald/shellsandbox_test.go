package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
)

// runCommandReply is a scripted model's two answers for one turn: a run_command call, then
// the turn's last words.
func runCommandReply(command string) [][]string {
	return [][]string{
		{fmt.Sprintf(`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"run_command","arguments":{"command":%q}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`, command)},
		{`{"model":"m","message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
	}
}

// finishTurn sends text and waits for the turn's end, approving every approval asked on the
// way; it returns how many were asked.
func finishTurn(t *testing.T, stream *client.Stream, threadID, text string) int {
	t.Helper()
	ctx := t.Context()
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": threadID, "text": text}, nil); err != nil {
		t.Fatal(err)
	}
	asked := 0
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "approval.requested":
				asked++
				var a struct{ ID string }
				_ = n.Decode(&a)
				if err := stream.Call(ctx, "approval.respond", map[string]any{"approval_id": a.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				return asked
			}
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
}

// TestAFullAutoCommandIsBoundedByTheKernel_REQ_SBX_006: in `full-auto` a command line that
// starts inside the worktree runs unasked, whatever paths it names — the policy reads where
// it starts, not what it writes. With the sandbox enforcing, the kernel refuses its write
// outside the worktree; its write inside lands, and its temporary directory is its own.
// Back in `normal` the thread's shell is not confined: the same line, approved, writes.
func TestAFullAutoCommandIsBoundedByTheKernel_REQ_SBX_006(t *testing.T) {
	needSandbox(t)
	repo, env := gitRepo(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// No word of the line is built at run time: one that is asks in every mode (REQ-SEC-005).
	line := func(name string) string {
		return "touch " + filepath.Join(outside, name) + "; echo inside > made-" + name + ".txt; printenv TMPDIR > tmp-" + name + ".txt"
	}
	ollama := &scriptedOllama{replies: append(runCommandReply(line("auto")), runCommandReply(line("normal"))...)}
	stream, _ := agentDaemon(t, ollama.server(t).URL, env...)
	ctx := t.Context()

	var thread acpThread
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo, "worktree": true}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Sandbox != nil {
		t.Errorf("a thread in normal mode says sandbox %q", *thread.Sandbox)
	}
	var wt struct{ Path string }
	if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": thread.WorktreeID}, &wt); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.update", map[string]any{"thread_id": thread.ID, "mode": "full-auto"}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Sandbox == nil || *thread.Sandbox != "confined" {
		t.Errorf("a full-auto thread's sandbox = %v, want confined", thread.Sandbox)
	}

	if asked := finishTurn(t, stream, thread.ID, "do it"); asked != 0 {
		t.Fatalf("full-auto asked %d times for a command starting in its worktree", asked)
	}
	if _, err := os.Stat(filepath.Join(outside, "auto")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the full-auto command wrote outside its worktree (%v)", err)
	}
	if got, err := os.ReadFile(filepath.Join(wt.Path, "made-auto.txt")); err != nil || strings.TrimSpace(string(got)) != "inside" {
		t.Errorf("the write inside the worktree: %q, %v", got, err)
	}
	tmp, err := os.ReadFile(filepath.Join(wt.Path, "tmp-auto.txt"))
	runtimeDir := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "umbral")
	if want := filepath.Join("shells", thread.ID, "tmp"); err != nil || !strings.HasSuffix(strings.TrimSpace(string(tmp)), want) {
		t.Errorf("the confined shell's TMPDIR = %q, %v; want one ending in %s under %s", tmp, err, want, runtimeDir)
	}

	if err := stream.Call(ctx, "thread.update", map[string]any{"thread_id": thread.ID, "mode": "normal"}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Sandbox != nil {
		t.Errorf("back in normal mode the thread says sandbox %q", *thread.Sandbox)
	}
	if asked := finishTurn(t, stream, thread.ID, "again"); asked != 1 {
		t.Fatalf("normal mode asked %d times, want once", asked)
	}
	if _, err := os.Stat(filepath.Join(outside, "normal")); err != nil {
		t.Errorf("an approved command in normal mode was confined: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(wt.Path, "tmp-normal.txt")); strings.Contains(string(got), filepath.Join("shells", thread.ID)) {
		t.Errorf("the unconfined shell kept the confined one's TMPDIR: %q", got)
	}
}
