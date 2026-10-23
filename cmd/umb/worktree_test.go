package main

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func worktreeReplies(merge fakeReply) map[string]fakeReply {
	return map[string]fakeReply{
		"worktree.list": func(map[string]any) (any, string) {
			return map[string]any{"items": []any{map[string]any{
				"id": "wtr_1", "state": "active", "thread_id": "thr_1", "path": "/data/wt/thr_1",
			}}}, ""
		},
		"worktree.diff": func(map[string]any) (any, string) {
			return map[string]any{"worktree_id": "wtr_1", "revision": "rev9", "files": []any{map[string]any{
				"path": "a.txt", "status": "modified", "binary": false,
				"hunks": []any{map[string]any{"id": "h1", "header": "@@ -1 +1 @@", "lines": []any{"-one", "+uno\x1b[2J"}}},
			}}}, ""
		},
		"worktree.merge": merge,
		"worktree.discard": func(map[string]any) (any, string) {
			return map[string]any{"id": "wtr_1", "state": "discarded"}, ""
		},
	}
}

// TestUmbWorktreeMergesExactlyTheDiffItShowed_REQ_WT_004: `umb worktree merge` shows the diff,
// asks, and passes the revision it showed; without a terminal and without --yes it merges
// nothing. `discard` asks the same way and sends confirm. `list` and `diff` read.
func TestUmbWorktreeMergesExactlyTheDiffItShowed_REQ_WT_004(t *testing.T) {
	defer func(prev func(io.Reader) bool) { stdinIsTerminal = prev }(stdinIsTerminal)
	stdinIsTerminal = func(io.Reader) bool { return false }
	d := serveRecordingDaemon(t, worktreeReplies(func(map[string]any) (any, string) {
		return map[string]any{"commit": "c0ffee", "worktree": map[string]any{"id": "wtr_1", "state": "merged"}}, ""
	}))
	sock := []string{"--socket", d.socket, "--no-autostart"}
	umb := func(want int, args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		requireExit(t, runIO(append(args, sock...), strings.NewReader(""), &stdout, &stderr), want, &stderr)
		return stdout.String(), stderr.String()
	}

	if out, _ := umb(exitOK, "worktree", "list", "--thread", "thr_1"); !strings.Contains(out, "wtr_1") || !strings.Contains(out, "active") {
		t.Errorf("list printed %q", out)
	}
	out, _ := umb(exitOK, "worktree", "diff", "wtr_1")
	if !strings.Contains(out, "modified a.txt") || !strings.Contains(out, "-one") || !strings.Contains(out, "revision rev9") {
		t.Errorf("diff printed %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("diff wrote a terminal escape from the repository: %q", out)
	}

	if _, errOut := umb(exitFailure, "worktree", "merge", "wtr_1"); !strings.Contains(errOut, "--yes") {
		t.Errorf("merge without a terminal said %q", errOut)
	}
	if n := len(d.callsTo("worktree.merge")); n != 0 {
		t.Fatalf("an unconfirmed merge was sent %d time(s)", n)
	}
	if out, _ := umb(exitOK, "worktree", "merge", "wtr_1", "--yes", "--message", "land it"); out != "c0ffee\n" {
		t.Errorf("merge printed %q, want the commit alone", out)
	}
	merges := d.callsTo("worktree.merge")
	if len(merges) != 1 || !reflect.DeepEqual(merges[0].Params, map[string]any{"worktree_id": "wtr_1", "revision": "rev9", "message": "land it"}) {
		t.Fatalf("worktree.merge sent %+v", merges)
	}

	umb(exitFailure, "worktree", "discard", "wtr_1")
	if n := len(d.callsTo("worktree.discard")); n != 0 {
		t.Fatal("an unconfirmed discard was sent")
	}
	if out, _ := umb(exitOK, "worktree", "discard", "wtr_1", "--yes"); out != "discarded wtr_1\n" {
		t.Errorf("discard printed %q", out)
	}
	if p := d.callsTo("worktree.discard")[0].Params; p["confirm"] != true || p["worktree_id"] != "wtr_1" {
		t.Errorf("worktree.discard sent %v", p)
	}
	stdinIsTerminal = func(io.Reader) bool { return true }
	var stdout, stderr bytes.Buffer
	requireExit(t, runIO(append([]string{"worktree", "discard", "wtr_1"}, sock...), strings.NewReader("y\n"), &stdout, &stderr), exitOK, &stderr)
}

// TestUmbWorktreeExplainsARefusal_REQ_WT_004: a refused merge names its reason, a conflict's
// paths and what git said, and exits 1.
func TestUmbWorktreeExplainsARefusal_REQ_WT_004(t *testing.T) {
	t.Parallel()
	d := serveRecordingDaemon(t, worktreeReplies(func(map[string]any) (any, string) {
		return map[string]any{"reason": "conflict", "paths": []any{"a.txt", "b\x1b.txt"}, "output": "error: could not apply\nhint: x"}, "CONFLICT"
	}))
	var stdout, stderr bytes.Buffer
	requireExit(t, runIO([]string{"worktree", "merge", "wtr_1", "--yes", "--socket", d.socket, "--no-autostart"},
		strings.NewReader(""), &stdout, &stderr), exitFailure, &stderr)
	got := stderr.String()
	for _, want := range []string{"(conflict)", "conflict: a.txt", "git: error: could not apply", "git: hint: x"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("a path's escape reached the terminal: %q", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout %q, want nothing", stdout.String())
	}
}

// TestUmbWorktreeWaitsForALongMerge_REQ_WT_004: a merge may take as long as the daemon's
// commit_timeout (60 s by default, hooks and signing included) and a diff as long as the
// repository needs, so neither is cut at callTimeout: umb would exit failing while the daemon
// lands the commit. Ctrl-C still ends them. A list keeps the short bound.
func TestUmbWorktreeWaitsForALongMerge_REQ_WT_004(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"worktree.merge", "worktree.diff", "worktree.discard"} {
		ctx, cancel := worktreeCallContext(t.Context(), method)
		if deadline, ok := ctx.Deadline(); ok {
			t.Errorf("%s runs under a deadline %s away", method, time.Until(deadline).Round(time.Second))
		}
		cancel()
	}
	ctx, cancel := worktreeCallContext(t.Context(), "worktree.list")
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("worktree.list runs without a deadline")
	}
}
