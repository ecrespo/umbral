package main

import (
	"context"
	"flag"
	"strings"
)

type notifyResult struct {
	Shown  bool   `json:"shown"`
	Reason string `json:"reason"`
}

// cmdNotify is `umb notify TITLE --source S [--body B] [--sound none|done|request]`: a hook's
// way to raise a notification (REQ-NTF-001, API Spec §5.35). The daemon normalizes,
// rate-limits and answers a typed reason; a notification not shown is an answer, not a
// failure.
func cmdNotify(ctx context.Context, args []string, stdout, stderr *printer) int {
	fs := flag.NewFlagSet("umb notify", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	source := fs.String("source", "", "who asks, e.g. claude-code: 1-80 of A-Z a-z 0-9 : . _ -")
	body := fs.String("body", "", "the text under the title")
	sound := fs.String("sound", "", "none, done or request; a hint the client may play")
	pos, rest, err := parseInterleaved(fs, args)
	if err != nil {
		return exitFailure
	}
	title := strings.TrimSpace(strings.Join(append(pos, rest...), " "))
	if title == "" || *source == "" {
		stderr.println(`umb notify: expected a title and --source, e.g. umb notify "build done" --source ci-hook`)
		return exitFailure
	}

	params := map[string]any{"title": title, "source": *source}
	if *body != "" {
		params["body"] = *body
	}
	if *sound != "" {
		params["sound"] = *sound
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result notifyResult
	if err := c.Call(callCtx, "notification.show", params, &result); err != nil {
		return callFailed(stderr, "umb notify: ", err)
	}
	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}
	if result.Shown {
		stdout.println("shown")
	} else {
		stdout.printf("not shown: %s\n", result.Reason)
	}
	return exitOK
}
