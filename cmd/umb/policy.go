package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

type explainTraceStep struct {
	Step    string `json:"step"`
	Matched bool   `json:"matched"`
	Decided bool   `json:"decided"`
	Detail  string `json:"detail,omitempty"`
	Note    string `json:"note,omitempty"`
}

type explainResult struct {
	Decision     string             `json:"decision"`
	DecidingRule string             `json:"deciding_rule"`
	Reason       string             `json:"reason,omitempty"`
	AllowAlways  bool               `json:"allow_always"`
	Trace        []explainTraceStep `json:"trace"`
}

// cmdPolicy is `umb policy explain` (REQ-SEC-009, API Spec §5.36): what the policy would
// decide for a candidate action, and why. The daemon evaluates; nothing runs. The target is
// the positionals joined with spaces; after `--` it may hold words that look like flags.
func cmdPolicy(ctx context.Context, args []string, stdout, stderr *printer) int {
	if len(args) == 0 || args[0] != "explain" {
		stderr.println("umb policy: the one subcommand is `umb policy explain`")
		return exitFailure
	}
	fs := flag.NewFlagSet("umb policy explain", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	thread := fs.String("thread", "", "a thread whose mode, write root and rules to use")
	mode := fs.String("mode", "", "ask, normal or auto-edit; overrides the thread's")
	tool := fs.String("tool", "", "the tool, e.g. run_command or write_file")
	risk := fs.String("risk", "", "ReadOnly, WriteFS, Exec or Network")
	tainted := fs.Bool("tainted", false, "the turn holds untrusted content")
	pos, rest, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	if *tool == "" || *risk == "" || (*mode == "" && *thread == "") {
		stderr.println(`umb policy explain: expected --tool, --risk and --mode or --thread, e.g.` + "\n" +
			`  umb policy explain --mode normal --tool run_command --risk Exec "rm -rf build"`)
		return exitFailure
	}

	params := map[string]any{"tool": *tool, "risk": *risk, "target": strings.Join(append(pos, rest...), " ")}
	if *thread != "" {
		params["thread_id"] = *thread
	}
	if *mode != "" {
		params["mode"] = *mode
	}
	if *tainted {
		params["tainted"] = true
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result explainResult
	if err := c.Call(callCtx, "policy.explain", params, &result); err != nil {
		return callFailed(stderr, "umb policy explain: ", err)
	}
	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}
	head := fmt.Sprintf("%s, decided by %s", result.Decision, result.DecidingRule)
	if result.Reason != "" {
		head += " (" + result.Reason + ")"
	}
	if !result.AllowAlways {
		head += "; never remembered as always"
	}
	stdout.println(head)
	for _, s := range result.Trace {
		mark := "-"
		switch {
		case s.Decided:
			mark = "decided"
		case s.Matched:
			mark = "matched"
		}
		line := fmt.Sprintf("  %-20s %-8s %s", s.Step, mark, s.Detail)
		if s.Note != "" {
			line += " (" + s.Note + ")"
		}
		stdout.println(strings.TrimRight(line, " "))
	}
	return exitOK
}
