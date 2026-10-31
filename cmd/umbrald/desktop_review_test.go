package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/desktop"
	"github.com/ecrespo/umbral/internal/desktop/adapters/vtframe"
)

// TestTheDesktopReviewsHunksAgainstARealDaemon_REQ_DSK_002: the desktop client's core, on a
// `desktop` connection to a real daemon and a real repository, reads a worktree's diff by hunk,
// rejects one, and merges what is left, so only the accepted hunk lands; it lists the worktree's
// files with the changed one marked (REQ-DSK-003), reads one and saves an edit with the digest it
// read, and a save with a stale digest writes nothing (delta `2026-10-hunks-file-tree`).
func TestTheDesktopReviewsHunksAgainstARealDaemon_REQ_DSK_002(t *testing.T) {
	repo, env := gitRepo(t)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line " + string(rune('a'+i%26))
	}
	writeFile(t, filepath.Join(repo, "notes.txt"), strings.Join(lines, "\n")+"\n")
	runGit(t, repo, env, "add", "-A")
	runGit(t, repo, env, "commit", "-q", "-m", "notes")

	srv := (&scriptedOllama{}).server(t)
	stream, ctx := agentDaemon(t, srv.URL, env...)
	var thread struct {
		WorktreeID string `json:"worktree_id"`
	}
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": repo, "worktree": true}, &thread); err != nil {
		t.Fatal(err)
	}
	var wt struct{ Path string }
	if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": thread.WorktreeID}, &wt); err != nil {
		t.Fatal(err)
	}
	// Two changes far apart are two hunks: the first line and the last.
	edited := slices30(lines)
	edited[0], edited[29] = "first, kept", "last, rejected"
	writeFile(t, filepath.Join(wt.Path, "notes.txt"), strings.Join(edited, "\n")+"\n")

	c, err := client.Connect(ctx, client.Options{
		SocketPath: filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "umbral", "umbral.sock"), NoAutostart: true, ClientKind: client.ClientKindDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := desktop.New(client.NewUnboundedStream(c), &recordingEmitter{ch: make(chan recorded, 64)}, vtframe.New)
	t.Cleanup(func() { _ = b.Close() })

	type diffResult struct {
		Revision string `json:"revision"`
		Files    []struct {
			Path  string `json:"path"`
			Hunks []struct {
				ID    string   `json:"id"`
				Lines []string `json:"lines"`
			} `json:"hunks"`
		} `json:"files"`
	}
	call := func(method string, params map[string]any, out any) error {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		res, err := b.Call(method, raw)
		if err != nil || out == nil {
			return err
		}
		return json.Unmarshal(res, out)
	}
	var diff diffResult
	if err := call("worktree.diff", map[string]any{"worktree_id": thread.WorktreeID}, &diff); err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || len(diff.Files[0].Hunks) != 2 {
		t.Fatalf("diff %+v, want notes.txt with two hunks", diff)
	}
	var left diffResult
	if err := call("worktree.reject_hunks", map[string]any{
		"worktree_id": thread.WorktreeID, "revision": diff.Revision, "hunk_ids": []string{diff.Files[0].Hunks[1].ID},
	}, &left); err != nil {
		t.Fatalf("worktree.reject_hunks: %v", err)
	}
	if len(left.Files) != 1 || len(left.Files[0].Hunks) != 1 || left.Files[0].Hunks[0].ID != diff.Files[0].Hunks[0].ID {
		t.Fatalf("after the reject %+v, want the first hunk alone", left)
	}

	var files struct {
		Items []struct {
			Path   string  `json:"path"`
			Status *string `json:"status"`
		} `json:"items"`
	}
	if err := call("worktree.files", map[string]any{"worktree_id": thread.WorktreeID}, &files); err != nil {
		t.Fatalf("worktree.files: %v", err)
	}
	marks := map[string]string{}
	for _, f := range files.Items {
		marks[f.Path] = "unchanged"
		if f.Status != nil {
			marks[f.Path] = *f.Status
		}
	}
	if marks["notes.txt"] != "modified" || marks["README"] != "unchanged" {
		t.Errorf("the tree marks %v, want notes.txt modified and README unchanged", marks)
	}
	var file struct {
		Content  *string `json:"content"`
		SHA256   string  `json:"sha256"`
		Editable bool    `json:"editable"`
	}
	if err := call("worktree.read_file", map[string]any{"worktree_id": thread.WorktreeID, "path": "README"}, &file); err != nil {
		t.Fatalf("worktree.read_file: %v", err)
	}
	if file.Content == nil || *file.Content != "the main copy\n" || !file.Editable {
		t.Fatalf("README read %+v", file)
	}
	if err := call("worktree.write_file", map[string]any{
		"worktree_id": thread.WorktreeID, "path": "README", "content": "edited in the window\n", "expected_sha256": file.SHA256,
	}, nil); err != nil {
		t.Fatalf("worktree.write_file: %v", err)
	}
	err = call("worktree.write_file", map[string]any{
		"worktree_id": thread.WorktreeID, "path": "README", "content": "a stale save\n", "expected_sha256": file.SHA256,
	}, nil)
	if client.DomainCode(err) != "CONFLICT" {
		t.Errorf("a save with a stale digest: %v, want CONFLICT", err)
	}

	var final diffResult
	if err := call("worktree.diff", map[string]any{"worktree_id": thread.WorktreeID}, &final); err != nil {
		t.Fatal(err)
	}
	if err := call("worktree.merge", map[string]any{"worktree_id": thread.WorktreeID, "revision": final.Revision}, nil); err != nil {
		t.Fatalf("worktree.merge: %v", err)
	}
	want := slices30(lines)
	want[0] = "first, kept"
	if body, _ := os.ReadFile(filepath.Join(repo, "notes.txt")); string(body) != strings.Join(want, "\n")+"\n" {
		t.Errorf("main holds\n%s\nwant only the accepted hunk", body)
	}
	if body, _ := os.ReadFile(filepath.Join(repo, "README")); string(body) != "edited in the window\n" {
		t.Errorf("main's README is %q, want the window's edit", body)
	}
}

func slices30(s []string) []string { return append([]string(nil), s...) }
