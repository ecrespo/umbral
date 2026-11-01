package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ecrespo/umbral/internal/client"
)

// worktreeIDKey is the parameter every worktree.* call but list names.
const worktreeIDKey = "worktree_id"

const worktreeUsage = "umb worktree: list [--thread THREAD | --workspace W] | diff WORKTREE | " +
	"merge WORKTREE [--message M] [--yes] | discard WORKTREE [--yes]"

// worktreeDiff is worktree.diff's answer (API Spec §5.41).
type worktreeDiff struct {
	WorktreeID string `json:"worktree_id"`
	Revision   string `json:"revision"`
	Files      []struct {
		Path   string `json:"path"`
		Status string `json:"status"`
		Binary bool   `json:"binary"`
		Hunks  []struct {
			ID     string   `json:"id"`
			Header string   `json:"header"`
			Lines  []string `json:"lines"`
		} `json:"hunks"`
	} `json:"files"`
}

// cmdWorktree is `umb worktree` (REQ-WT-003…005, API Spec §5.41): read a thread's worktree, and
// land or drop it. A merge shows the diff it is about to land, asks, and passes the revision it
// showed, so work the agent adds after the review is refused rather than landed unseen
// (delta `2026-10-worktree-engine`, decision 19). A discard asks too: it loses the work.
func cmdWorktree(ctx context.Context, args []string, stdin io.Reader, stdout, stderr *printer) int {
	if len(args) == 0 {
		stderr.println(worktreeUsage)
		return exitFailure
	}
	sub := args[0]
	fs := flag.NewFlagSet("umb worktree "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	thread := fs.String("thread", "", "list: the worktrees of this thread")
	workspace := fs.String("workspace", "", "list: the worktrees of this workspace's threads")
	message := fs.String("message", "", "merge: the commit message, instead of the generated one")
	yes := fs.Bool("yes", false, "merge, discard: do it without asking")
	pos, rest, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	pos = append(pos, rest...)
	prefix := "umb worktree " + sub + ": "

	switch sub {
	case skillList:
		if len(pos) != 0 {
			stderr.printf("%sunexpected argument %q\n", prefix, pos[0])
			return exitFailure
		}
	case "diff", "merge", "discard":
		if len(pos) != 1 {
			stderr.printf("%sexpected one WORKTREE, e.g. umb worktree %s wtr_01K5…\n", prefix, sub)
			return exitFailure
		}
	default:
		stderr.println(worktreeUsage)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	call := func(method string, params map[string]any, out any) error {
		callCtx, cancel := worktreeCallContext(ctx, method)
		defer cancel()
		return c.Call(callCtx, method, params, out)
	}

	switch sub {
	case skillList:
		params := map[string]any{}
		if *thread != "" {
			params["thread_id"] = *thread
		}
		if *workspace != "" {
			params["workspace_id"] = *workspace
		}
		var result map[string]any
		if err := call("worktree.list", params, &result); err != nil {
			return worktreeFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, result)
		}
		items, _ := result["items"].([]any)
		if len(items) == 0 {
			stdout.println("(none)")
		}
		for _, item := range items {
			w, _ := item.(map[string]any)
			stdout.printf("%v  %-9v  %v  %v\n", w["id"], w["state"], w["thread_id"], printable(fmt.Sprint(w["path"])))
		}
		return exitOK

	case "diff":
		var d worktreeDiff
		if err := call("worktree.diff", map[string]any{worktreeIDKey: pos[0]}, &d); err != nil {
			return worktreeFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, d)
		}
		printDiff(stdout, d)
		return exitOK

	case "merge":
		var d worktreeDiff
		if err := call("worktree.diff", map[string]any{worktreeIDKey: pos[0]}, &d); err != nil {
			return worktreeFailed(stderr, prefix, err)
		}
		// The review goes to stderr: stdout keeps the commit, the one value a script needs.
		printDiff(stderr, d)
		if !*yes && !confirmWorktree(stdin, stderr, "merge", fmt.Sprintf("Land these %d file(s) from %s?", len(d.Files), pos[0])) {
			return exitFailure
		}
		params := map[string]any{worktreeIDKey: pos[0], "revision": d.Revision}
		if *message != "" {
			params["message"] = *message
		}
		var result map[string]any
		if err := call("worktree.merge", params, &result); err != nil {
			return worktreeFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, result)
		}
		if commit, ok := result["commit"].(string); ok {
			stdout.println(commit)
		} else {
			stderr.printf("%s%s changed nothing; no commit was made\n", prefix, pos[0])
		}
		return exitOK

	default: // discard
		if !*yes && !confirmWorktree(stdin, stderr, "discard", fmt.Sprintf("Discard %s and every change in it?", pos[0])) {
			return exitFailure
		}
		var result map[string]any
		if err := call("worktree.discard", map[string]any{worktreeIDKey: pos[0], "confirm": true}, &result); err != nil {
			return worktreeFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, result)
		}
		stdout.printf("discarded %s\n", pos[0])
		return exitOK
	}
}

// worktreeCallContext bounds a worktree call. A list is quick; a diff, a merge and a discard
// run git in the daemon, which bounds them itself (a merge by its commit_timeout, 60 s by
// default, hooks and signing included), so they wait for the answer or Ctrl-C. Cut at
// callTimeout, umb would exit failing while the daemon went on and landed the commit.
func worktreeCallContext(ctx context.Context, method string) (context.Context, context.CancelFunc) {
	if method == "worktree.list" {
		return context.WithTimeout(ctx, callTimeout)
	}
	return context.WithCancel(ctx)
}

// printDiff writes the diff as a reviewer reads it: each file with its status, then its hunks,
// and the revision a merge of exactly this content would pass. Every line goes through
// printable, since a repository's content reaches the terminal here.
func printDiff(out *printer, d worktreeDiff) {
	if len(d.Files) == 0 {
		out.println("(no changes)")
	}
	for _, file := range d.Files {
		out.printf("%s %s\n", file.Status, printable(file.Path))
		if file.Binary {
			out.println("  (binary)")
		}
		for _, h := range file.Hunks {
			out.println(printable(h.Header))
			for _, line := range h.Lines {
				out.println(printable(line))
			}
		}
	}
	out.printf("revision %s\n", d.Revision)
}

// confirmWorktree is a "y" typed on a terminal; with none to ask on the answer is no, and the
// user is told to pass --yes.
func confirmWorktree(stdin io.Reader, stderr *printer, verb, question string) bool {
	if !stdinIsTerminal(stdin) {
		stderr.printf("umb worktree %s: no terminal to confirm on; read the above, then run again with --yes\n", verb)
		return false
	}
	stderr.printf("%s [y/N] ", question)
	if answeredYes(stdin, stderr) {
		return true
	}
	stderr.printf("umb worktree %s: nothing done\n", verb)
	return false
}

// worktreeFailed explains a refusal by its reason (API Spec §3): the conflicting paths of a
// squash, or what git said when the commit failed.
func worktreeFailed(stderr *printer, prefix string, err error) int {
	var rpcErr *client.Error
	if !errors.As(err, &rpcErr) || rpcErr.Reason == "" {
		return callFailed(stderr, prefix, err)
	}
	stderr.printf("umb: %s%s (%s)\n", prefix, printable(rpcErr.Message), rpcErr.Reason)
	for _, p := range rpcErr.Paths {
		stderr.printf("  conflict: %s\n", printable(p))
	}
	if rpcErr.Output != "" {
		for _, line := range strings.Split(strings.TrimRight(rpcErr.Output, "\n"), "\n") {
			stderr.printf("  git: %s\n", printable(line))
		}
	}
	return exitFailure
}
