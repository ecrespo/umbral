// Command umbrald is the Umbral daemon: it owns the PTYs, their VT emulation, the
// blocks derived from shell integration, the agent runtime and the model gateway.
//
// This file is the composition root. Per Art. 3 it is the only place allowed to wire
// modules together; every other package talks through ports or bus events.
//
// What works today: the daemon opens its database, applies migrations, runs the restart
// recovery of Data Model §6, owns PTY sessions with their emulators, and serves system.*
// and session.* over the 0600 Unix socket. Output streaming to clients arrives in T-F0-06
// and blocks in T-F0-09.
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
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/adapters/blockstore"
	"github.com/ecrespo/umbral/internal/sessions/adapters/ghostty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/pty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/shellinteg"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "0.0.0-dev"

// Exit codes from sysexits.h, so shell callers can tell the failures apart.
const (
	exitUsage       = 64 // EX_USAGE: a bad command line
	exitDataErr     = 65 // EX_DATAERR: the database is unusable, for example a newer schema
	exitUnavailable = 69 // EX_UNAVAILABLE: the socket could not be served
	exitCantCreate  = 73 // EX_CANTCREAT: a required file or directory could not be created
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
	socketPath := fs.String("socket", "", "JSON-RPC socket (default $XDG_RUNTIME_DIR/umbral/umbral.sock)")
	oneShot := fs.Bool("check", false, "open the database, recover and exit without serving")
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
		return exitCantCreate
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

	logger.Info("database ready",
		slog.String("version", buildVersion()),
		slog.String("database", db.Path()),
		slog.Int("schema_version", schemaVersion),
		slog.Int64("sessions_recovered", report.SessionsExited),
		slog.Int64("blocks_abandoned", report.BlocksAbandoned))

	if *oneShot {
		return 0
	}

	socket, err := resolveSocketPath(*socketPath)
	if err != nil {
		logger.Error("cannot locate the socket", slog.Any("error", err))
		return exitCantCreate
	}
	tokenPath := filepath.Join(filepath.Dir(socket), api.TokenFileName)

	// The bus is created here, in the composition root, and handed to whoever needs it.
	// Art. 3 allows no other package to wire modules together.
	eventBus := bus.New()
	defer eventBus.Close()

	// The sessions module gets its adapters here and nowhere else: the PTY, the emulator
	// and the shell bootstrap are all injected, which is what keeps libghostty and
	// creack/pty confined to one directory each (Art. 3).
	blocks, err := blockstore.New(db)
	if err != nil {
		logger.Error("cannot build the block store", slog.Any("error", err))
		return exitCantCreate
	}
	defer func() { _ = blocks.Close() }()

	sessionService, err := sessions.New(sessions.Config{
		Store:     db,
		Bus:       eventBus,
		NewPTY:    pty.Open,
		NewEmu:    ghostty.NewEmulator,
		Bootstrap: shellinteg.Adapter{},
		Blocks:    blocks,
		// A scanner per session: it carries the state of a sequence split across two
		// PTY reads, so one shared between sessions would mix their streams.
		NewScanner: func() sessports.Scanner { return shellinteg.NewScanner() },
		Logger:     logger,
	})
	if err != nil {
		logger.Error("cannot build the sessions module", slog.Any("error", err))
		return exitCantCreate
	}
	// Every PTY is closed on the way out so no shell is orphaned. The rows are repaired on
	// the next start by store.Recover.
	defer sessionService.Shutdown()

	server, err := api.Listen(ctx, api.Config{
		SocketPath:    socket,
		TokenPath:     tokenPath,
		DaemonVersion: buildVersion(),
		Status:        statusFromStore(db),
		Sessions:      sessionService,
		Bus:           eventBus,
		Logger:        logger,
	})
	if err != nil {
		logger.Error("cannot open the socket", slog.Any("error", err))
		return exitCantCreate
	}
	defer func() {
		if err := server.Close(); err != nil {
			logger.Error("cannot close the socket", slog.Any("error", err))
		}
	}()

	// Forward module events to connected clients (API Spec §6).
	go server.Notify(ctx)

	logger.Info("umbrald listening",
		slog.String("socket", server.SocketPath()),
		slog.String("token_file", tokenPath))

	if err := server.Serve(ctx); err != nil {
		logger.Error("the socket stopped serving", slog.Any("error", err))
		return exitUnavailable
	}

	logger.Info("umbrald stopped")
	return 0
}

// resolveSocketPath honours an explicit -socket flag and otherwise asks api for the
// platform default.
func resolveSocketPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	return api.DefaultSocketPath()
}

// statusFromStore answers system.status from the database. It is a closure rather than a
// method so that api keeps knowing nothing about store, and so that T-F0-05 can replace
// the counts with live ones without touching the api package.
func statusFromStore(db *store.Store) api.StatusFunc {
	return func(ctx context.Context) (api.StatusResult, error) {
		var alive, running int
		// threads exists from migration 0001, so the count is real rather than a
		// placeholder, even though nothing writes to that table before T-F1-01.
		err := db.DB().QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM sessions WHERE state = 'alive'),
			       (SELECT count(*) FROM threads  WHERE state = 'running')`).
			Scan(&alive, &running)
		if err != nil {
			return api.StatusResult{}, fmt.Errorf("count sessions and threads: %w", err)
		}
		return api.StatusResult{SessionsAlive: alive, ThreadsRunning: running}, nil
	}
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
