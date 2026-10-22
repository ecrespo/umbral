package main

import (
	"errors"
	"flag"
	"os"
)

// `umb pane report_state` and `umb pane release_state` (API Spec §5.31, REQ-INT-002,
// REQ-INT-005): how an integration's hook tells Umbral what the agent in its pane is doing.
// Like every tree command they are one call to the method of the same name, underscore
// included, so the command and the method cannot drift apart.
//
// The pane defaults to $UMBRAL_PANE_ID, which the daemon injects into every pane
// (REQ-INT-001): a hook runs inside the pane it reports for. The socket does not default to
// $UMBRAL_SOCKET_PATH; a hook passes `--socket "$UMBRAL_SOCKET_PATH"` when it needs to, so no
// other `umb` command started from a pane is redirected to that pane's daemon unasked.

const (
	subReportState  = "report_state"
	subReleaseState = "release_state"

	keyPaneID = "pane_id"
	keySource = "source"
)

func paneStateCommands() map[string]treeCommand {
	return map[string]treeCommand{
		subReportState: {
			args:  []string{"state", "pane?"},
			about: "report idle, working, blocked or done for a pane (default $UMBRAL_PANE_ID)",
			flags: func(fs *flag.FlagSet) func() (map[string]any, error) {
				source := fs.String(keySource, "", "the reporter's stable name, [A-Za-z0-9:._-] (required)")
				agent := fs.String("agent", "", "the agent being reported on")
				message := fs.String("message", "", "a short note shown with the state")
				seq := fs.Int64("seq", 0, "the report's sequence number; a stale one is discarded")
				return func() (map[string]any, error) {
					p := map[string]any{keySource: *source}
					if *agent != "" {
						p["agent"] = *agent
					}
					if *message != "" {
						p["message"] = *message
					}
					// Sent only when given: no seq means "not numbered", which 0 is not.
					fs.Visit(func(f *flag.Flag) {
						if f.Name == "seq" {
							p["seq"] = *seq
						}
					})
					return p, nil
				}
			},
			params: func(pos []string, extra map[string]any) (map[string]any, error) {
				pane, err := paneOrEnv(pos[1:])
				if err != nil {
					return nil, err
				}
				return merge(map[string]any{keyPaneID: pane, "state": pos[0]}, extra), nil
			},
			human: printNothing,
		},
		subReleaseState: {
			args:  []string{"pane?"},
			about: "give a pane back to Umbral's own detection (default $UMBRAL_PANE_ID)",
			flags: func(fs *flag.FlagSet) func() (map[string]any, error) {
				source := fs.String(keySource, "", "the reporter's stable name (required)")
				return func() (map[string]any, error) { return map[string]any{keySource: *source}, nil }
			},
			params: func(pos []string, extra map[string]any) (map[string]any, error) {
				pane, err := paneOrEnv(pos)
				if err != nil {
					return nil, err
				}
				return merge(map[string]any{keyPaneID: pane}, extra), nil
			},
			human: printNothing,
		},
	}
}

// paneOrEnv is the pane named on the command line, or the one this process runs in.
func paneOrEnv(pos []string) (string, error) {
	if len(pos) > 0 {
		return pos[0], nil
	}
	if pane := os.Getenv("UMBRAL_PANE_ID"); pane != "" {
		return pane, nil
	}
	return "", errors.New("no pane given and $UMBRAL_PANE_ID is not set: run it inside a pane, or name one")
}

// printNothing is the human form of `{}`: a hook's success is its exit code.
func printNothing(_, _ *printer, _ map[string]any) {}
