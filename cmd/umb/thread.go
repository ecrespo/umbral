package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// The subcommands of `umb thread` (REQ-CLI-009, delta `2026-10-cli-thread-wait`).
const (
	threadCreate = "create"
	threadSend   = "send"
	threadWait   = "wait"
	threadGet    = "get"
	threadCancel = "cancel"
)

const threadUsage = "umb thread: create [CWD] [--mode M] [--title T] [--model M] [--agent A] [--worktree] [--workspace W] | " +
	"send THREAD TEXT… [--wait STATES] [--timeout D] | wait THREAD --until STATES [--timeout D] | " +
	"get THREAD | cancel THREAD"

// defaultThreadWait is how long `send --wait` and `wait` wait when --timeout is not given. API
// §8 allows 1 s to 1 h; a turn of a local model on a real task takes minutes.
const defaultThreadWait = 10 * time.Minute

// cmdThread is `umb thread` (REQ-CLI-009): a script drives a thread with one command per step,
// each one call to the method of the same name, and waits without polling — `send --wait` and
// `wait` hold the connection open until the daemon answers (REQ-AUT-001). Each prints the one
// value the next step needs: the thread id, the turn id, the state.
func cmdThread(ctx context.Context, args []string, stdout, stderr *printer) int {
	if len(args) == 0 {
		stderr.println(threadUsage)
		return exitFailure
	}
	sub := args[0]
	fs := flag.NewFlagSet("umb thread "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	mode := fs.String("mode", "", "create: the thread's mode, as the daemon names it (API §4)")
	title := fs.String("title", "", "create: the thread's title")
	model := fs.String("model", "", "create: the model, provider/name")
	agent := fs.String("agent", "", "create: host this configured ACP agent instead of Umbral's own (REQ-ACP-001)")
	worktree := fs.Bool("worktree", false, "create: give the thread its own git worktree (REQ-WT-001)")
	workspace := fs.String("workspace", os.Getenv("UMBRAL_WORKSPACE_ID"), "create: the workspace the thread belongs to; the pane's own by default")
	wait := fs.String("wait", "", "send: wait for one of these states, comma-separated: idle,done,blocked,stopped")
	until := fs.String("until", "", "wait: the states that end it, comma-separated: idle,done,blocked,stopped")
	timeout := fs.Duration("timeout", defaultThreadWait, "send --wait and wait: how long to wait, 1s to 1h")
	pos, rest, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	pos = append(pos, rest...)

	var method string
	params := map[string]any{}
	callFor := callTimeout
	switch sub {
	case threadCreate:
		if len(pos) > 1 {
			stderr.printf("umb thread create: unexpected argument %q\n", pos[1])
			return exitFailure
		}
		dir := "."
		if len(pos) == 1 {
			dir = pos[0]
		}
		// The daemon's working directory is not this shell's.
		abs, err := filepath.Abs(dir)
		if err != nil {
			stderr.printf("umb thread create: %v\n", err)
			return exitFailure
		}
		method, params["cwd"] = "thread.create", abs
		for key, v := range map[string]string{"mode": *mode, keyTitle: *title, "model": *model, "workspace": *workspace, "agent": *agent} {
			if v != "" {
				params[key] = v
			}
		}
		if *worktree {
			params["worktree"] = true
		}
	case threadSend:
		if len(pos) < 2 {
			stderr.println("umb thread send: expected THREAD TEXT…, e.g. umb thread send thr_01K5… \"fix the test\"")
			return exitFailure
		}
		method, params["thread_id"], params["text"] = "thread.send", pos[0], strings.Join(pos[1:], " ")
		if *wait != "" {
			params["wait"] = map[string]any{"until": splitStates(*wait), "timeout_ms": timeout.Milliseconds()}
			callFor = *timeout + callTimeout
		}
	case threadWait:
		if len(pos) != 1 || *until == "" {
			stderr.println("umb thread wait: expected THREAD --until STATES, e.g. umb thread wait thr_01K5… --until done,blocked")
			return exitFailure
		}
		method = "thread.wait"
		params["thread_id"], params["until"], params["timeout_ms"] = pos[0], splitStates(*until), timeout.Milliseconds()
		callFor = *timeout + callTimeout
	case threadGet, threadCancel:
		if len(pos) != 1 {
			stderr.printf("umb thread %s: expected one THREAD\n", sub)
			return exitFailure
		}
		method, params["thread_id"] = "thread."+sub, pos[0]
		if sub == threadGet {
			params["include_messages"], params["limit"] = true, threadGetPage
		}
	default:
		stderr.println(threadUsage)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	callCtx, cancel := context.WithTimeout(ctx, callFor)
	defer cancel()

	var result map[string]any
	if err := callThread(callCtx, c, method, params, &result); err != nil {
		var rpcErr *client.Error
		if errors.As(err, &rpcErr) && rpcErr.DomainCode == "TIMEOUT" {
			stderr.printf("umb thread %s: no target state within %s; the thread was last %s\n", sub, *timeout, rpcErr.LastState)
			return exitFailure
		}
		return callFailed(stderr, "umb thread "+sub+": ", err)
	}
	if refused, ok := result["worktree_refused"].(map[string]any); ok {
		// The thread exists and works in its own directory (REQ-WT-008); say so where a script's
		// stdout is not read.
		stderr.printf("umb thread create: the thread works without a worktree: %s (%v)\n",
			printable(fmt.Sprint(refused["message"])), printable(fmt.Sprint(refused["reason"])))
	}
	if hosted, ok := result["agent"].(string); ok && hosted != "" {
		// REQ-ACP-007: what Umbral cannot see is said where the user reads, not where a script
		// takes the id.
		stderr.printf("umb thread create: %s is an external agent; Umbral does not record its own network traffic or file reads (egress_visibility %s); %s can read the repository's main copy and every other thread's worktree\n",
			printable(hosted), printable(fmt.Sprint(result["egress_visibility"])), printable(hosted))
		if gone, ok := result["agent_unavailable"].(map[string]any); ok {
			stderr.printf("umb thread create: %s did not start: %s (%v)\n", printable(hosted),
				printable(fmt.Sprint(gone["message"])), printable(fmt.Sprint(gone["reason"])))
		}
	}
	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}
	switch sub {
	case threadCreate:
		stdout.printf("%v\n", result["id"])
	case threadSend:
		if state, ok := result["final_state"]; ok {
			stdout.printf("%v %v\n", result["turn_id"], state)
		} else {
			stdout.printf("%v\n", result["turn_id"])
		}
	case threadWait:
		stdout.printf("%v\n", result["state"])
	case threadGet:
		printThread(stdout, result)
	case threadCancel:
		stdout.printf("cancelled %s\n", pos[0])
	}
	return exitOK
}

// threadGetPage is the largest page API §5.23 serves.
const threadGetPage = 1000

// callThread makes the call, and for `thread.get` follows `next_cursor` to the last page,
// gathering every message into one result: pages run oldest first, so the first alone would
// leave out the turn's answer on any thread longer than a page.
func callThread(ctx context.Context, c *client.Client, method string, params map[string]any, result *map[string]any) error {
	if err := c.Call(ctx, method, params, result); err != nil || method != "thread.get" {
		return err
	}
	messages, _ := (*result)["messages"].([]any)
	for {
		cursor, _ := (*result)["next_cursor"].(string)
		if cursor == "" {
			break
		}
		params["cursor"] = cursor
		var page map[string]any
		if err := c.Call(ctx, method, params, &page); err != nil {
			return err
		}
		more, _ := page["messages"].([]any)
		messages = append(messages, more...)
		(*result)["next_cursor"] = page["next_cursor"]
	}
	delete(*result, "next_cursor")
	(*result)["messages"] = messages
	return nil
}

// splitStates turns "done, blocked" into the list a wait takes; the daemon validates the names.
func splitStates(s string) []string {
	var out []string
	for _, state := range strings.Split(s, ",") {
		if state = strings.TrimSpace(state); state != "" {
			out = append(out, state)
		}
	}
	return out
}

// printThread is the human form of `thread.get`: the thread's id and state, then each message
// with its role, oldest first, escaped as `umb skill show` escapes a body so model text cannot
// drive the terminal, and with its extra lines indented so a line of model text cannot pass
// for a message of its own. A script reads `--json`.
func printThread(out *printer, result map[string]any) {
	// The result is the Thread itself, with its messages beside it (API §5.23).
	id, _ := result["id"].(string)
	state, _ := result["attention_state"].(string)
	out.printf("%s  %s\n", printable(id), printable(state))
	messages, _ := result["messages"].([]any)
	for _, m := range messages {
		obj, _ := m.(map[string]any)
		role, _ := obj["role"].(string)
		content, _ := obj["content"].(string)
		out.printf("%s: %s\n", printable(role), strings.ReplaceAll(printableText(content), "\n", "\n  "))
	}
}
