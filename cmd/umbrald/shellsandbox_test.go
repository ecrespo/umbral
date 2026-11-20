package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/client"
	sandboxdomain "github.com/ecrespo/umbral/internal/sandbox/domain"
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

// recordingSandbox enforces and keeps the profile it was asked to confine with.
type recordingSandbox struct{ profile *sandboxdomain.Profile }

func (recordingSandbox) Status() sandboxdomain.Status {
	return sandboxdomain.Status{State: sandboxdomain.StateEnforcing, Mechanism: "test"}
}

func (r recordingSandbox) Command(p sandboxdomain.Profile, argv []string) ([]string, error) {
	*r.profile = p
	return append([]string{"confined"}, argv...), nil
}

// fakeThreads and fakeWorktrees are the thread's record and its worktree, as the confiner
// reads them.
type fakeThreads struct {
	thread agentsdomain.Thread
	err    error
}

func (f fakeThreads) Thread(context.Context, string) (agentsdomain.Thread, error) {
	return f.thread, f.err
}

type fakeWorktrees struct {
	w      agentsports.Worktree
	active bool
	err    error
}

func (f fakeWorktrees) Create(context.Context, string, string, string) (agentsports.Worktree, error) {
	return agentsports.Worktree{}, errors.New("not in this test")
}

func (f fakeWorktrees) Active(context.Context, string) (agentsports.Worktree, bool, error) {
	return f.w, f.active, f.err
}

// shellBench is a bound shell confiner over a data and a runtime directory on disk, with one
// thread whose worktree is `<data>/worktrees/r/<thread>`.
func shellBench(t *testing.T, thread agentsdomain.Thread, threadsErr error, wts fakeWorktrees) (*shellConfiner, *sandboxdomain.Profile, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, run := filepath.Join(base, "data"), filepath.Join(base, "run")
	worktree := filepath.Join(data, "worktrees", "r", thread.ID)
	for _, d := range []string{worktree, filepath.Join(run, "shellinteg-1")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if wts.w.Path == "" {
		wts.w = agentsports.Worktree{ID: "wt_1", Path: worktree, RepoRoot: filepath.Join(base, "repo")}
	}
	profile := &sandboxdomain.Profile{}
	c := &shellConfiner{}
	c.bind(&shellSandbox{
		acpConfiner: acpConfiner{
			sandbox: recordingSandbox{profile: profile}, protected: []string{data, run},
			helper: "/usr/bin/umbrald", worktrees: filepath.Join(data, "worktrees"), homes: filepath.Join(run, "acp"),
		},
		shells: filepath.Join(run, "shells"), threads: fakeThreads{thread: thread, err: threadsErr}, worktrees: wts,
	})
	return c, profile, worktree, run
}

// TestWhetherAShellIsConfinedIsReadFromTheThread_REQ_SBX_006: the record says full-auto for
// Umbral's own thread with the flag set and an active worktree, and for nothing else. A
// record that cannot be read answers yes, so that Confine refuses instead of a plain shell
// being started; and before the confiner is bound nothing is confined.
func TestWhetherAShellIsConfinedIsReadFromTheThread_REQ_SBX_006(t *testing.T) {
	own := agentsdomain.Thread{ID: "thr_1", Cwd: t.TempDir(), Mode: "normal", FullAuto: true}
	plain, hosted := own, own
	plain.FullAuto = false
	hosted.Agent = "opencode"
	for name, c := range map[string]struct {
		thread agentsdomain.Thread
		err    error
		wts    fakeWorktrees
		want   bool
	}{
		"full-auto in an active worktree": {thread: own, wts: fakeWorktrees{active: true}, want: true},
		"the flag without a worktree":     {thread: own, wts: fakeWorktrees{active: false}},
		"normal mode":                     {thread: plain, wts: fakeWorktrees{active: true}},
		"a hosted thread":                 {thread: hosted, wts: fakeWorktrees{active: true}},
		"an unknown thread":               {thread: own, err: agentsdomain.ErrNotFound, wts: fakeWorktrees{active: true}},
		"a record that cannot be read":    {thread: own, err: errors.New("disk"), wts: fakeWorktrees{active: true}, want: true},
		"a worktree that cannot be read":  {thread: own, wts: fakeWorktrees{err: errors.New("disk")}, want: true},
	} {
		confiner, _, _, _ := shellBench(t, c.thread, c.err, c.wts)
		if !confiner.Enforces() {
			t.Fatalf("%s: a bound confiner over an enforcing sandbox does not enforce", name)
		}
		if got := confiner.FullAuto(t.Context(), c.thread.ID); got != c.want {
			t.Errorf("%s: FullAuto = %v, want %v", name, got, c.want)
		}
		if _, _, err := confiner.Confine(t.Context(), c.thread.ID, nil, []string{"/bin/sh"}); (err == nil) != (c.want && c.err == nil && c.wts.err == nil) {
			t.Errorf("%s: Confine err = %v", name, err)
		}
	}
	unbound := &shellConfiner{}
	if unbound.Enforces() || unbound.FullAuto(t.Context(), "thr_1") {
		t.Error("an unbound confiner confines")
	}
	if _, _, err := unbound.Confine(t.Context(), "thr_1", nil, []string{"/bin/sh"}); err == nil {
		t.Error("an unbound confiner confined a shell")
	}
}

// TestAShellIsConfinedToItsWorktreeWhereverItStarts_REQ_SBX_006: the profile is made from the
// thread's worktree and never from where the shell starts — the thread's own directory can
// be made to resolve outside the worktree by a command of the thread's. It writes the
// worktree and a directory of the shell's own, reads the shell's start-up files, and gives
// the shell that directory for its temporary files.
func TestAShellIsConfinedToItsWorktreeWhereverItStarts_REQ_SBX_006(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The thread's directory is one that resolves outside its worktree.
	thread := agentsdomain.Thread{ID: "thr_1", Cwd: outside, Mode: "normal", FullAuto: true}
	confiner, profile, worktree, run := shellBench(t, thread, nil, fakeWorktrees{active: true})
	boot := filepath.Join(run, "shellinteg-1")
	argv, env, err := confiner.Confine(t.Context(), thread.ID, []string{boot}, []string{"/bin/zsh", "-i"})
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(run, "shells", thread.ID)
	if len(argv) != 3 || argv[0] != "confined" {
		t.Errorf("argv = %q", argv)
	}
	if want := []string{worktree, own}; len(profile.Write) != 2 || profile.Write[0] != want[0] || profile.Write[1] != want[1] {
		t.Errorf("the profile writes %v, want %v", profile.Write, want)
	}
	for _, w := range profile.Write {
		if sandboxdomain.Within(outside, w) || sandboxdomain.Within(w, outside) {
			t.Errorf("the profile writes %s, which reaches the directory outside the worktree", w)
		}
	}
	if len(profile.Read) != 1 || profile.Read[0] != boot {
		t.Errorf("the profile reads %v, want the shell's start-up directory", profile.Read)
	}
	tmp := filepath.Join(own, "tmp")
	if want := []string{"TMPDIR=" + tmp, "TMPPREFIX=" + filepath.Join(tmp, "zsh")}; len(env) != 2 || env[0] != want[0] || env[1] != want[1] {
		t.Errorf("env = %q, want %q", env, want)
	}
	if info, err := os.Stat(tmp); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the shell's temporary directory: %v %v", info, err)
	}

	// What the last shell left is gone, and a link put where the directory goes is not
	// followed: the shell is not started.
	if err := os.WriteFile(filepath.Join(tmp, "left"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := confiner.Confine(t.Context(), thread.ID, nil, []string{"/bin/sh"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "left")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the last shell's file is still in the new shell's directory: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(run, "shells")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(run, "shells")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := confiner.Confine(t.Context(), thread.ID, nil, []string{"/bin/sh"}); !errors.Is(err, sandboxdomain.ErrProfile) {
		t.Errorf("a shells directory that is a link elsewhere: err = %v, want ErrProfile", err)
	}
	// A thread id is a name, never a path: the directory emptied at each launch is built from
	// it, and one that climbs would empty something else.
	if err := os.Remove(filepath.Join(run, "shells")); err != nil {
		t.Fatal(err)
	}
	bystander := filepath.Join(run, "thr_1", "keep")
	if err := os.MkdirAll(bystander, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"a path": "../thr_1", "nothing": ""} {
		if _, _, err := confiner.Confine(t.Context(), id, nil, []string{"/bin/sh"}); err == nil {
			t.Errorf("%s as a thread id was confined", name)
		}
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("a thread id that climbs emptied a directory outside the shells': %v", err)
	}
}

// TestAFullAutoCommandCannotSwitchItsConfinementOff_REQ_SBX_006: found in review. The thread's
// directory is a subdirectory of its repository, and so of its worktree, where the confined
// shell can rename it. A command that swaps it for a link out of the worktree made the next
// command's shell read as "not in the worktree" and start unconfined. Confinement is not
// read from where the shell starts: after the swap a write outside is still refused.
func TestAFullAutoCommandCannotSwitchItsConfinementOff_REQ_SBX_006(t *testing.T) {
	needSandbox(t)
	repo, env := gitRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "keep"), "x\n")
	runGit(t, repo, env, "add", "-A")
	runGit(t, repo, env, "commit", "-q", "-m", "sub")
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(outside, "escaped")
	var thread acpThread
	swap := "cd .. && mv sub was-sub && ln -s " + outside + " sub"
	ollama := &scriptedOllama{replies: [][]string{
		runCommandCall(swap), runCommandCall("touch " + escaped), runCommandCall("touch " + escaped),
		{`{"model":"m","message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
	}}
	stream, _ := agentDaemon(t, ollama.server(t).URL, env...)
	ctx := t.Context()
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": sub, "worktree": true}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.update", map[string]any{"thread_id": thread.ID, "mode": "full-auto"}, &thread); err != nil {
		t.Fatal(err)
	}
	asked := finishTurn(t, stream, thread.ID, "do it")
	if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after swapping its directory for a link, the thread wrote outside its worktree (asked %d times): %v", asked, err)
	}
	var wt struct{ Path string }
	if err := stream.Call(ctx, "worktree.get", map[string]any{"worktree_id": thread.WorktreeID}, &wt); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(wt.Path, "sub")); err != nil || target != outside {
		t.Fatalf("the swap did not happen, so the test proved nothing: %q, %v", target, err)
	}
}

// runCommandCall is a scripted model's answer that calls run_command.
func runCommandCall(command string) []string {
	arg, _ := json.Marshal(command)
	return []string{fmt.Sprintf(`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"run_command","arguments":{"command":%s}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`, arg)}
}
