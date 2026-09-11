// Command umbrald is the Umbral daemon: it owns the PTYs, their VT emulation, the
// blocks derived from shell integration, the agent runtime and the model gateway.
//
// This file is the composition root. Per Art. 3 it is the only place allowed to wire
// modules together; every other package talks through ports or bus events.
//
// T-F0-01 leaves it as a runnable skeleton. The JSON-RPC listener arrives in T-F0-03.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "0.0.0-dev"

// exitUsage is the sysexits.h EX_USAGE code, used for a bad command line.
const exitUsage = 64

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("umbrald", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the daemon version and exit")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *showVersion {
		if _, err := fmt.Fprintln(stdout, buildVersion()); err != nil {
			return 1
		}
		return 0
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	logger.Info("umbrald is not implemented yet",
		slog.String("version", buildVersion()),
		slog.String("next_task", "T-F0-03: JSON-RPC listener, authentication and bus"))
	return 0
}

// buildVersion reports the linker-provided version, falling back to the VCS revision
// that the Go toolchain stamps into the binary.
func buildVersion() string {
	if version != "0.0.0-dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return version + "+" + s.Value[:min(len(s.Value), 12)]
		}
	}
	return version
}
