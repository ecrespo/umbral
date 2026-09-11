// Command umbrald is the Umbral daemon: it owns the PTYs, their VT emulation, the
// blocks derived from shell integration, the agent runtime and the model gateway.
//
// This file is the composition root. Per Art. 3 it is the only place allowed to wire
// modules together; every other package talks through ports or bus events.
//
// What works today: the daemon opens its database, applies migrations and runs the
// restart recovery of Data Model §6. The JSON-RPC listener arrives in T-F0-03.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "0.0.0-dev"

// Exit codes from sysexits.h, so shell callers can tell the failures apart.
const (
	exitUsage     = 64 // EX_USAGE: a bad command line
	exitDataErr   = 65 // EX_DATAERR: the database is unusable, for example a newer schema
	exitCantCreat = 73 // EX_CANTCREAT: the data directory could not be created
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("umbrald", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the daemon version and exit")
	dbPath := fs.String("db", "", "database file (default $XDG_DATA_HOME/umbral/umbral.db)")
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

	db, err := store.Open(ctx, store.Options{Path: *dbPath})
	if err != nil {
		logger.Error("cannot open the database", slog.Any("error", err))
		if errors.Is(err, store.ErrSchemaTooNew) {
			return exitDataErr
		}
		return exitCantCreat
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("cannot close the database", slog.Any("error", err))
		}
	}()

	// Data Model §6: every PTY died with the previous process, so any session still
	// marked alive is stale. Blocks that were open become abandoned, never deleted.
	report, err := db.Recover(ctx, time.Now())
	if err != nil {
		logger.Error("recovery after restart failed", slog.Any("error", err))
		return exitDataErr
	}

	schemaVersion, err := db.SchemaVersion(ctx)
	if err != nil {
		logger.Error("cannot read the schema version", slog.Any("error", err))
		return exitDataErr
	}

	logger.Info("umbrald started",
		slog.String("version", buildVersion()),
		slog.String("database", db.Path()),
		slog.Int("schema_version", schemaVersion),
		slog.Int64("sessions_recovered", report.SessionsExited),
		slog.Int64("blocks_abandoned", report.BlocksAbandoned))

	logger.Info("there is nothing to serve yet",
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
