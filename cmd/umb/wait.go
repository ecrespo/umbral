package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// waitItem mirrors one entry of `wait.list` (API Spec §5.33).
type waitItem struct {
	WaitID       string `json:"wait_id"`
	ConnectionID string `json:"connection_id"`
	Target       struct {
		ThreadID  string `json:"thread_id,omitempty"`
		BlockID   string `json:"block_id,omitempty"`
		SessionID string `json:"session_id,omitempty"`
	} `json:"target"`
	Until   []string `json:"until"`
	Regex   *string  `json:"regex"`
	AgeMS   int64    `json:"age_ms"`
	Stalled bool     `json:"stalled"`
}

type waitListResult struct {
	Items []waitItem `json:"items"`
}

// line is how `umb wait ls` prints one wait: its id, target, what ends it, its age, the
// thread's stall mark and the connection holding it.
func (w waitItem) line() string {
	parts := []string{w.WaitID}
	switch {
	case w.Target.ThreadID != "":
		parts = append(parts, "thread "+w.Target.ThreadID)
	case w.Target.BlockID != "":
		parts = append(parts, "block "+w.Target.BlockID)
	default:
		parts = append(parts, "session "+w.Target.SessionID)
	}
	if w.Regex != nil {
		parts = append(parts, fmt.Sprintf("regex %q", *w.Regex))
	} else {
		parts = append(parts, "until "+strings.Join(w.Until, ","))
	}
	parts = append(parts, fmt.Sprintf("%.1fs", float64(w.AgeMS)/1000))
	if w.Stalled {
		parts = append(parts, "stalled")
	}
	return strings.Join(append(parts, w.ConnectionID), "  ")
}

const (
	waitLs     = "ls"
	waitCancel = "cancel"
)

// cmdWait is `umb wait ls` and `umb wait cancel WAIT_ID` (REQ-AUT-006, REQ-AUT-007): what is
// waiting on the daemon, and a way to end one wait without touching what it observes.
func cmdWait(ctx context.Context, args []string, stdout, stderr *printer) int {
	if len(args) == 0 || (args[0] != waitLs && args[0] != waitCancel) {
		stderr.println("umb wait: want `umb wait ls` or `umb wait cancel WAIT_ID`")
		return exitFailure
	}
	sub := args[0]
	fs := flag.NewFlagSet("umb wait "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	pos, rest, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	pos = append(pos, rest...)
	switch {
	case sub == waitLs && len(pos) > 0:
		stderr.printf("umb wait ls: unexpected argument %q\n", pos[0])
		return exitFailure
	case sub == waitCancel && len(pos) != 1:
		stderr.println("umb wait cancel: expected one WAIT_ID, e.g. umb wait cancel wt_01K5…; `umb wait ls` lists them")
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	if sub == waitCancel {
		var result struct{}
		if err := c.Call(callCtx, "wait.cancel", map[string]any{"wait_id": pos[0]}, &result); err != nil {
			return callFailed(stderr, "umb wait cancel: ", err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, result)
		}
		stdout.printf("cancelled %s\n", pos[0])
		return exitOK
	}

	var result waitListResult
	if err := c.Call(callCtx, "wait.list", map[string]any{}, &result); err != nil {
		return callFailed(stderr, "umb wait ls: ", err)
	}
	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}
	if len(result.Items) == 0 {
		stdout.println("no open waits")
		return exitOK
	}
	for _, w := range result.Items {
		stdout.println(w.line())
	}
	return exitOK
}
