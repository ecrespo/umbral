package main

import (
	"flag"

	"github.com/ecrespo/umbral/internal/api"
)

// cmdAPI serves `umb api schema` (REQ-API-004, API Spec §5.37).
//
// `cmd/umb` imports `internal/api`, which `internal/client` may not. That is the boundary
// rules working rather than being routed around: Tech Design §5.2 keeps the *client library*
// off the server's package so the two sides of the protocol cannot quietly share a type and
// drift together, and it lets `cmd/*` wire anything because a composition root is the one
// place allowed to. This command wires nothing into the client path — it reads a document and
// prints it — so the rule it would have broken is not one it comes near.
//
// It prints the protocol compiled into this binary and never opens the socket. That is not
// a shortcut: the schema describes what this build speaks, so asking a daemon for it would
// answer a different question — and a client that cannot find out what the protocol is
// without a running daemon is exactly the client REQ-API-004 exists to help. The daemon
// answers `api.schema` with the same document for the case where the question really is
// "what does *that* daemon speak".
func cmdAPI(args []string, stdout, stderr *printer) int {
	if len(args) == 0 {
		stderr.print("umb api: expected a subcommand: schema\n")
		return exitFailure
	}

	switch args[0] {
	case "schema":
		return cmdAPISchema(args[1:], stdout, stderr)
	default:
		stderr.printf("umb api: unknown subcommand %q\n", args[0])
		return exitFailure
	}
}

func cmdAPISchema(args []string, stdout, stderr *printer) int {
	fs := flag.NewFlagSet("umb api schema", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	asJSON := fs.Bool("json", false, "print the schema as JSON")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if !*asJSON {
		stderr.print("umb api schema: --json is required; the schema has no other form\n")
		return exitFailure
	}

	// An empty Config: every module reports itself unwired, so `served` is false
	// throughout. The method set, the parameter shapes and the error table do not depend
	// on configuration — they are what this binary was built with — and a CLI has no
	// modules to report on anyway. `umb status` is where a client asks what a daemon is
	// running; this is where it asks what the protocol is.
	return printJSON(stdout, stderr, api.Schema(api.Config{}))
}
