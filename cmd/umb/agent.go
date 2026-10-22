package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
)

// agentExplainResult mirrors `agent.explain` (API Spec §5.39).
type agentExplainResult struct {
	PaneID        string `json:"pane_id"`
	DetectedAgent *struct {
		Name        string `json:"name"`
		StateSource string `json:"state_source"`
	} `json:"detected_agent"`
	Manifest *struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Origin      string `json:"origin"`
	} `json:"manifest"`
	Rule *struct {
		Kind       string `json:"kind"`
		Pattern    string `json:"pattern"`
		PID        int    `json:"pid"`
		Executable string `json:"executable"`
	} `json:"rule"`
	StateAuthority struct {
		AttentionState string  `json:"attention_state"`
		StateSource    *string `json:"state_source"`
	} `json:"state_authority"`
	Skipped []struct {
		File   string `json:"file"`
		Reason string `json:"reason"`
	} `json:"skipped_manifests"`
}

// cmdAgent is `umb agent explain PANE` (REQ-XAG-003): which third-party agent detection found
// in a pane, what matched, and who decides the pane's state. Every field a manifest file or
// a process supplied is escaped before it reaches the terminal.
func cmdAgent(ctx context.Context, args []string, stdout, stderr *printer) int {
	if len(args) == 0 || args[0] != "explain" {
		stderr.println("umb agent: want `umb agent explain PANE`")
		return exitFailure
	}
	fs := flag.NewFlagSet("umb agent explain", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	pos, rest, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	pos = append(pos, rest...)
	if len(pos) != 1 {
		stderr.println("umb agent explain: expected one PANE, e.g. umb agent explain w1:p2")
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	// --json prints the answer as it came, members this client does not know included.
	var raw json.RawMessage
	if err := c.Call(callCtx, "agent.explain", map[string]any{"pane_id": pos[0]}, &raw); err != nil {
		return callFailed(stderr, "umb agent explain: ", err)
	}
	if f.asJSON {
		return printJSON(stdout, stderr, raw)
	}
	var result agentExplainResult
	if err := json.Unmarshal(raw, &result); err != nil {
		stderr.printf("umb agent explain: the daemon's answer is not an explanation: %v\n", err)
		return exitFailure
	}
	stdout.printf("pane      %s\n", printable(result.PaneID))
	if a, m, r := result.DetectedAgent, result.Manifest, result.Rule; a != nil && m != nil && r != nil {
		stdout.printf("agent     %s (%s), could report through %s\n",
			printable(a.Name), printable(m.DisplayName), printable(a.StateSource))
		stdout.printf("manifest  %s\n", printable(m.Origin))
		stdout.printf("rule      %s %s matched pid %d (%s)\n",
			printable(r.Kind), printable(fmt.Sprintf("%q", r.Pattern)), r.PID, printable(r.Executable))
	} else {
		stdout.println("agent     none detected")
	}
	source := "no source"
	if s := result.StateAuthority.StateSource; s != nil {
		source = printable(*s)
	}
	stdout.printf("state     %s, decided by %s\n", printable(result.StateAuthority.AttentionState), source)
	for _, s := range result.Skipped {
		stdout.printf("skipped   %s: %s\n", printable(s.File), printable(s.Reason))
	}
	return exitOK
}
