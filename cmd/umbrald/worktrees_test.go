package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
)

// gitRepo makes a repository with one commit on main, and the environment that keeps every git
// run — the test's and the daemon's — off the developer's own configuration.
func gitRepo(t *testing.T) (repo string, env []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "[user]\n\tname = Umbral Test\n\temail = test@umbral.invalid\n[commit]\n\tgpgsign = false\n")
	env = []string{"GIT_CONFIG_GLOBAL=" + global, "GIT_CONFIG_NOSYSTEM=1"}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "README"), "the main copy\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "first"}} {
		runGit(t, repo, env, args...)
	}
	return repo, env
}

func runGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestAThreadWritesOnlyInItsWorktree_REQ_WT_002 drives a thread created with `worktree: true`
// through a real daemon and a real git. Its turn writes a relative path, which lands in the
// worktree and not in the main copy; a write to the main copy by its absolute path is outside
// the write root, so even `auto-edit` asks for it and nothing is written; the diff shows the
// worktree's file, and the merge, passing the diff's revision, lands it on main as one commit.
func TestAThreadWritesOnlyInItsWorktree_REQ_WT_002(t *testing.T) {
	repo, env := gitRepo(t)
	escape := filepath.Join(repo, "escape.txt")
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"made.txt","content":"from the agent\n"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
		{`{"model":"m","message":{"role":"assistant","content":"written"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"` + escape + `","content":"escaped\n"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
	}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL, env...)

	var thread struct {
		ID         string `json:"id"`
		WorktreeID string `json:"worktree_id"`
	}
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo, "worktree": true, "mode": "auto-edit"}, &thread); err != nil {
		t.Fatal(err)
	}
	var wt struct {
		ID, Path, State, Branch string
	}
	if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": thread.WorktreeID}, &wt); err != nil {
		t.Fatal(err)
	}
	if wt.State != "active" || strings.HasPrefix(wt.Path, repo) || !strings.HasPrefix(wt.Path, os.Getenv("XDG_DATA_HOME")) {
		t.Fatalf("worktree %+v, want an active one under XDG_DATA_HOME, outside %s", wt, repo)
	}

	// policy.explain decides with the directories the turn will: the worktree's.
	for target, want := range map[string]string{"made.txt": "allow", escape: "ask"} {
		var d struct{ Decision string }
		if err := stream.Call(ctx, "policy.explain", map[string]any{
			"thread_id": thread.ID, "tool": "write_file", "risk": "WriteFS", "target": target,
		}, &d); err != nil {
			t.Fatal(err)
		}
		if d.Decision != want {
			t.Errorf("policy.explain write %s = %s, want %s as the turn decides", target, d.Decision, want)
		}
	}

	sendAndWait(ctx, t, stream, thread.ID, "make the file", "thread.turn_finished")
	if body, err := os.ReadFile(filepath.Join(wt.Path, "made.txt")); err != nil || string(body) != "from the agent\n" {
		t.Fatalf("the worktree holds %q (%v)", body, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the turn wrote in the main copy")
	}

	sendAndWait(ctx, t, stream, thread.ID, "now write outside", "approval.requested")
	if _, err := os.Stat(escape); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a write outside the worktree ran without approval")
	}
	if err := stream.Call(ctx, "thread.cancel", map[string]any{"thread_id": thread.ID}, nil); err != nil {
		t.Fatal(err)
	}

	var diff struct {
		Revision string
		Files    []struct{ Path, Status string }
	}
	if err := stream.Call(ctx, "worktree.diff", map[string]any{"worktree_id": wt.ID}, &diff); err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "made.txt" || diff.Files[0].Status != "added" {
		t.Fatalf("diff %+v, want made.txt added", diff)
	}
	var merged struct {
		Commit   *string `json:"commit"`
		Worktree struct{ State string }
	}
	if err := stream.Call(ctx, "worktree.merge", map[string]any{"worktree_id": wt.ID, "revision": diff.Revision}, &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Commit == nil || merged.Worktree.State != "merged" {
		t.Fatalf("merge %+v", merged)
	}
	if head := runGit(t, repo, env, "rev-parse", "HEAD"); head != *merged.Commit {
		t.Fatalf("main is at %s, the merge made %s", head, *merged.Commit)
	}
	if body, err := os.ReadFile(filepath.Join(repo, "made.txt")); err != nil || string(body) != "from the agent\n" {
		t.Fatalf("after the merge the main copy holds %q (%v)", body, err)
	}
	if status := runGit(t, repo, env, "status", "--porcelain"); status != "" {
		t.Fatalf("the main copy is not clean after the merge: %q", status)
	}
}

// sendAndWait sends text to the thread and waits for the notification named until.
func sendAndWait(ctx context.Context, t *testing.T, stream *client.Stream, threadID, text, until string) {
	t.Helper()
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": threadID, "text": text}, nil); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			if n.Method == until {
				return
			}
		case <-ctx.Done():
			t.Fatalf("no %s", until)
		}
	}
}

// TestUnknownWorkspaceIsRefused_REQ_ORC_003: thread.create takes an open workspace only.
func TestUnknownWorkspaceIsRefused_REQ_ORC_003(t *testing.T) {
	ollama := &scriptedOllama{}
	stream, ctx := agentDaemon(t, ollama.server(t).URL)
	err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "workspace": "w99"}, nil)
	var rpcErr *client.Error
	if !errors.As(err, &rpcErr) || rpcErr.DomainCode != "VALIDATION_ERROR" {
		t.Fatalf("thread.create in w99 = %v, want VALIDATION_ERROR", err)
	}
}

// TestAThreadWhoseWorktreeIsRefusedKeepsWorking_REQ_WT_008: thread.create with a worktree in a
// directory outside any repository answers the thread, without a worktree and with git's
// refusal named, and the thread is there to be read and sent to.
func TestAThreadWhoseWorktreeIsRefusedKeepsWorking_REQ_WT_008(t *testing.T) {
	_, env := gitRepo(t)
	ollama := &scriptedOllama{}
	stream, ctx := agentDaemon(t, ollama.server(t).URL, env...)
	var created struct {
		ID              string  `json:"id"`
		WorktreeID      *string `json:"worktree_id"`
		WorktreeRefused *struct {
			Reason, Message string
		} `json:"worktree_refused"`
	}
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir(), "worktree": true}, &created); err != nil {
		t.Fatalf("thread.create = %v, want the thread", err)
	}
	if created.ID == "" || created.WorktreeID != nil || created.WorktreeRefused == nil || created.WorktreeRefused.Reason != "not_git" ||
		strings.Contains(created.WorktreeRefused.Message, "agents:") {
		t.Fatalf("thread.create answered %+v (refusal %+v), want a thread with no worktree and reason not_git", created, created.WorktreeRefused)
	}
	var got struct{ ID string }
	if err := stream.Call(ctx, "thread.get", map[string]any{"thread_id": created.ID}, &got); err != nil || got.ID != created.ID {
		t.Fatalf("thread.get = %+v, %v", got, err)
	}
}
