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
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"

	remoteports "github.com/ecrespo/umbral/internal/remote/ports"

	"github.com/ecrespo/umbral/internal/agents"
	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/handoff"
	"github.com/ecrespo/umbral/internal/integrations"
	"github.com/ecrespo/umbral/internal/integrations/adapters/proctree"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/egresslog"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/modelstore"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/usagelog"
	"github.com/ecrespo/umbral/internal/notify"
	"github.com/ecrespo/umbral/internal/obs"
	"github.com/ecrespo/umbral/internal/security/adapters/keyring"
	secports "github.com/ecrespo/umbral/internal/security/ports"
	"github.com/ecrespo/umbral/internal/sessions"
	"github.com/ecrespo/umbral/internal/sessions/adapters/blockstore"
	"github.com/ecrespo/umbral/internal/sessions/adapters/ghostty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/pty"
	"github.com/ecrespo/umbral/internal/sessions/adapters/shellinteg"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/workspaces"
	"github.com/ecrespo/umbral/internal/workspaces/adapters/treestore"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "0.0.0-dev"

// Exit codes from sysexits.h, so shell callers can tell the failures apart.
const (
	exitOK          = 0  // served, or another daemon already owns this installation
	exitUsage       = 64 // EX_USAGE: a bad command line
	exitDataErr     = 65 // EX_DATAERR: the database is unusable, for example a newer schema
	exitUnavailable = 69 // EX_UNAVAILABLE: the socket could not be served
	exitCantCreate  = 73 // EX_CANTCREAT: a required file or directory could not be created
	exitTempFail    = 75 // EX_TEMPFAIL: the database is held by a daemon of another runtime directory
	exitConfig      = 78 // EX_CONFIG: the settings file is there and cannot be read
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (code int) {
	fs := flag.NewFlagSet("umbrald", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the daemon version and exit")
	dbPath := fs.String("db", "", "database file (default $XDG_DATA_HOME/umbral/umbral.db)")
	socketPath := fs.String("socket", "", "JSON-RPC socket (default $XDG_RUNTIME_DIR/umbral/umbral.sock)")
	oneShot := fs.Bool("check", false, "open the database, recover and exit without serving")
	takeover := fs.Bool("takeover", false, "take the live sessions of the daemon serving this runtime directory over")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *takeover && *oneShot {
		_, _ = fmt.Fprintln(stderr, "umbrald: -takeover serves; it cannot be combined with -check")
		return exitUsage
	}

	if *showVersion {
		if _, err := fmt.Fprintln(stdout, buildVersion()); err != nil {
			return 1
		}
		return 0
	}

	// Every line logged inside a turn carries its trace id (Art. 7, Tech §7.1).
	logger := slog.New(obs.LogHandler(slog.NewJSONHandler(stderr, nil)))

	socket, err := resolveSocketPath(*socketPath)
	if err != nil {
		logger.Error("cannot locate the socket", slog.Any("error", err))
		return exitCantCreate
	}

	// Settings before anything they configure, and a malformed file stops the daemon here
	// rather than after it has taken the lock and opened the database. A user who wrote
	// `pane_history = true` and got a daemon running with it off would believe their
	// screens were being captured when they were not; refusing is the honest answer.
	settingsPath, err := config.SettingsPath()
	if err != nil {
		logger.Error("cannot locate the settings file", slog.Any("error", err))
		return exitCantCreate
	}
	settings, err := config.LoadSettings(logger, settingsPath)
	if err != nil {
		logger.Error("the settings file cannot be read; refusing to start with defaults "+
			"the user did not ask for", slog.Any("error", err))
		return exitConfig
	}

	// The rule set in force (REQ-SEC-010): the built-in rules with the override directory on
	// top. An invalid file is warned about and skipped; it never stops the daemon or empties
	// the set.
	activeRules := loadRules(logger, settingsPath)
	redact := func(s string) string { return activeRules.Redact(s).Text }

	// The providers, with the same rule: a models file the schema refuses stops the daemon.
	// A plaintext key refuses only its own entry (REQ-SEC-004), and a keyring this machine
	// cannot reach disables only the providers that need it (REQ-SEC-008).
	modelsPath, err := config.ModelsPath()
	if err != nil {
		logger.Error("cannot locate the models file", slog.Any("error", err))
		return exitCantCreate
	}
	providers, err := loadProviders(ctx, logger, settingsPath, modelsPath, settings, keyring.New())
	if err != nil {
		logger.Error("the models file cannot be read; refusing to start with providers "+
			"the user did not configure", slog.Any("error", err))
		return exitConfig
	}

	// A handoff (REQ-HND-001, delta `2026-10-pty-handoff`): the running daemon pauses and passes
	// its socket, its locks and its live sessions, and exits once this one confirms. Every
	// failure until then leaves it serving (REQ-HND-002), and this daemon exits 75 naming the
	// step.
	var took *taken
	committed := false
	if *takeover && !liveHandoffSupported {
		logger.Error("live handoff is not supported on Windows")
		return exitUnavailable
	}
	if *takeover {
		if took, err = takeOver(ctx, filepath.Dir(socket), logger); err != nil {
			logger.Error("the handoff failed; the running daemon keeps serving",
				slog.String("step", handoffStep(err)), slog.Any("error", err))
			return exitTempFail
		}
		defer func() {
			if committed {
				return
			}
			// Whatever stopped the start, a failed takeover is EX_TEMPFAIL (delta decision 3).
			step := stepStart
			code = exitTempFail
			logger.Error("the handoff failed; the running daemon keeps serving", slog.String("step", step))
			took.close()
		}()
	}

	// Before the database, and before recovery above all. Recovery rewrites live rows on
	// the premise that the previous process is gone (Data Model §6); a second daemon
	// running it over a first one's sessions destroys state that is not stale. `umb`
	// autostarts, so two daemons starting at once is ordinary rather than exotic.
	var lock *config.InstanceLock
	if took != nil {
		lock = took.lock
	} else {
		lock, err = config.AcquireInstanceLock(filepath.Dir(socket))
	}
	if err != nil {
		if errors.Is(err, config.ErrAlreadyRunning) {
			// Not a failure: whoever holds the lock is serving this installation, which
			// is what the caller wanted. An autostarting `umb` retries and finds them.
			logger.Info("another umbrald already owns this installation; exiting",
				slog.String("socket", socket))
			return exitOK
		}
		logger.Error("cannot take the instance lock", slog.Any("error", err))
		return exitCantCreate
	}
	defer func() {
		if err := lock.Release(); err != nil {
			logger.Error("cannot release the instance lock", slog.Any("error", err))
		}
	}()

	// Under the lock, so this daemon is the only one of its installation and every
	// bootstrap directory beside the socket belongs to a process that is gone — no age
	// heuristic, no ownership check, no race to lose (REQ-TERM-012). Before the restore
	// below, because the shells that restore launches write their own directories here and
	// sweeping afterwards would delete the files they were started with.
	// Not on a handoff: the directories belong to the shells this daemon adopts.
	if took != nil {
		logger.Info("the bootstrap directories belong to the adopted shells; not sweeping")
	} else if removed, err := shellinteg.Sweep(filepath.Dir(socket)); err != nil {
		// Litter, not an outage: the user is waiting for a terminal, not for tidiness.
		logger.Warn("could not remove every bootstrap directory left by a previous run",
			slog.Int("removed", removed), slog.Any("error", err))
	} else if removed > 0 {
		logger.Info("removed the bootstrap directories left by a previous run",
			slog.Int("removed", removed))
	}

	// The database has an owner too (delta `2026-09-database-lock`). The instance lock above
	// serialises daemons of one runtime directory; recovery's premise is about this file, and
	// a daemon of another runtime directory pointed at it with -db would otherwise recover
	// over live sessions. Held elsewhere, nothing is serving the caller's socket and nothing
	// failed to be created, so this is EX_TEMPFAIL rather than 0 or 73.
	if took != nil {
		// The database the old daemon holds open, whose lock came with the handoff.
		if *dbPath != "" && *dbPath != took.dbPath {
			logger.Error("the running daemon uses another database",
				slog.String("database", took.dbPath), slog.String("asked", *dbPath))
			return exitTempFail
		}
		*dbPath = took.dbPath
	}
	if *dbPath == "" {
		if *dbPath, err = store.DefaultPath(); err != nil {
			logger.Error("cannot locate the database", slog.Any("error", err))
			return exitCantCreate
		}
	}
	var dbLock *config.InstanceLock
	if took != nil {
		dbLock = took.dbLock
	} else {
		dbLock, err = config.AcquireDatabaseLock(*dbPath)
	}
	if err != nil {
		if errors.Is(err, config.ErrDatabaseInUse) {
			logger.Error("another umbrald, of a different runtime directory, is using this "+
				"database; refusing to recover over its live sessions",
				slog.String("database", *dbPath), slog.String("socket", socket))
			return exitTempFail
		}
		logger.Error("cannot take the database lock", slog.Any("error", err))
		return exitCantCreate
	}
	defer func() {
		if err := dbLock.Release(); err != nil {
			logger.Error("cannot release the database lock", slog.Any("error", err))
		}
	}()

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
	//
	// Not on a handoff: every row it would repair is a session this daemon adopts, and the
	// old daemon refused the handoff while a turn ran (decision 5).
	var report store.RecoveryReport
	if took == nil {
		if report, err = db.Recover(ctx, time.Now()); err != nil {
			logger.Error("recovery after restart failed", slog.Any("error", err))
			return exitDataErr
		}
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
		slog.Int64("blocks_abandoned", report.BlocksAbandoned),
		slog.Int64("integration_settled", report.IntegrationSettled),
		slog.Int64("threads_stopped", report.ThreadsStopped),
		slog.Int64("approvals_expired", report.ApprovalsExpired),
		slog.Int64("pane_reports_cleared", report.PaneReportsCleared))

	if *oneShot {
		return 0
	}

	tokenPath := filepath.Join(filepath.Dir(socket), config.TokenFileName)

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
		Bootstrap: shellinteg.Adapter{Dir: filepath.Dir(socket)},
		Blocks:    blocks,
		// A scanner per session: it carries the state of a sequence split across two
		// PTY reads, so one shared between sessions would mix their streams.
		NewScanner: func() sessports.Scanner { return shellinteg.NewScanner() },
		// A handoff's sessions: the old daemon's PTYs, and their scanners where they were.
		AdoptPTY:      pty.Adopt,
		ResumeScanner: func(state []byte) (sessports.Scanner, error) { return shellinteg.ResumeScanner(state) },
		Logger:        logger,
		SocketPath:    socket,
		BinPath:       findUmb(logger),
	})
	if err != nil {
		logger.Error("cannot build the sessions module", slog.Any("error", err))
		return exitCantCreate
	}
	// Every PTY is closed on the way out so no shell is orphaned. The rows are repaired on
	// the next start by store.Recover.
	defer sessionService.Shutdown()
	if took != nil {
		if handoffFailAt == stepAdopt {
			err = errInjected
		} else {
			err = sessionService.Adopt(ctx, took.handed, took.masters)
		}
		if err != nil {
			logger.Error("the handoff failed; the running daemon keeps serving",
				slog.String("step", stepAdopt), slog.Any("error", err))
			committed = true // logged here; the deferred report would say "start"
			took.close()
			return exitTempFail
		}
		// Until the old daemon lets go the sessions are its own: a failure here touches none.
		defer func() {
			if !committed {
				sessionService.DropAdopted()
			}
		}()
	}

	blockReader, err := sessions.NewReader(sessions.ReaderConfig{Blocks: blocks})
	if err != nil {
		logger.Error("cannot build the block reader", slog.Any("error", err))
		return exitCantCreate
	}

	tree, err := treestore.New(db)
	if err != nil {
		logger.Error("cannot build the workspace store", slog.Any("error", err))
		return exitCantCreate
	}
	// The tree takes the sessions module through ports.Terminals, a two-method view of it:
	// a pane needs a terminal and needs to give it back, and nothing else. The assignment
	// is written through the port rather than the concrete service so go-arch-lint's deep
	// scan sees the dependency the boundary rules describe.
	var terminals wsports.Terminals = sessionService
	// REQ-TERM-010's capture reads a session's screen through a port declared in the
	// workspaces module's own terms, so neither module has to know the other's shapes.
	// Adapting them is a composition-root job, which is what this file is for.
	screens := wsports.Screens(screenReader{sessions: sessionService})
	// What third-party agent detection found in each session (T-F2-10), read by the tree for
	// `Pane.detected_agent` and written by the detector built below.
	agentLabels := integrations.NewAgentLabels()
	workspaceService, err := workspaces.New(workspaces.Config{
		Tree:        tree,
		Terminals:   terminals,
		Bus:         eventBus,
		Logger:      logger,
		PaneHistory: settings.PaneHistory,
		Screens:     screens,
		AgentLabels: agentLabels,
	})
	if err != nil {
		logger.Error("cannot build the workspace tree", slog.Any("error", err))
		return exitCantCreate
	}

	// The tree comes back before the socket opens (REQ-TERM-009).
	//
	// Order matters twice over. After `db.Recover` above, because that is what marks the
	// previous run's sessions `exited` — restoring first would attach fresh terminals to
	// panes whose old rows still claimed to be alive. And before `api.Listen`, because a
	// client that connected midway would watch panes appear one at a time with no
	// notification explaining them, and `session.snapshot` would report a tree that was
	// true for a moment.
	//
	// A restore that fails does not stop the daemon. The structure is a convenience; the
	// sessions a user starts next are not, and refusing to serve them because an old
	// workspace could not be rebuilt would turn a lost layout into a lost terminal.
	// Screens the setting no longer wants are deleted before anything can read them
	// (Data Model §2.4d, a privacy promise rather than housekeeping).
	// A takeover is a start too: the purge runs on it, and it deletes rows only.
	if err := workspaceService.PrepareHistory(ctx); err != nil {
		logger.Error("could not reconcile the stored pane screens with the setting",
			slog.Any("error", err))
	}
	if took != nil {
		// The panes keep their sessions: only the focus is read back (decision 5).
		if err := workspaceService.ResumeFocus(ctx); err != nil {
			logger.Error("could not read the focus back", slog.Any("error", err))
		}
	} else {
		if err := workspaceService.Restore(ctx); err != nil {
			logger.Error("could not restore the saved structure; starting with an empty tree",
				slog.Any("error", err))
		}
	}

	// The model catalog (REQ-LLM-002). Discovery runs in the background: a provider that is
	// slow or down must not hold up the socket.
	modelStore, err := modelstore.New(db)
	if err != nil {
		logger.Error("cannot build the model store", slog.Any("error", err))
		return exitCantCreate
	}
	egress, err := egresslog.New(db)
	if err != nil {
		logger.Error("cannot build the egress log", slog.Any("error", err))
		return exitCantCreate
	}
	usage, err := usagelog.New(db)
	if err != nil {
		logger.Error("cannot build the usage log", slog.Any("error", err))
		return exitCantCreate
	}
	// Traces and metrics (T-F1-18). Spans are made whether or not they are exported: their
	// trace ids join a turn's log lines. Export needs `[otel] endpoint`, which is local.
	metrics := obs.NewMetrics()
	gauges := &liveGauges{}
	telemetry, err := obs.NewTelemetry(ctx, obs.TelemetryConfig{
		Endpoint: settings.OTelEndpoint, Version: buildVersion(), Logger: logger, Metrics: metrics,
		WaitsActive: gauges.waitsActive, FramesRefused: gauges.framesRefused,
	})
	if err != nil {
		logger.Error("cannot build the telemetry", slog.Any("error", err))
		return exitCantCreate
	}
	defer func() {
		// What is pending goes to the collector before the daemon exits, within a bound: a
		// collector that is down must not hold the shutdown.
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(flush); err != nil {
			logger.Warn("telemetry did not flush", slog.Any("error", err))
		}
	}()

	// Signed rule updates (REQ-SEC-011): off unless `[update] rules_check` says otherwise, and
	// every request recorded in egress_log.
	// Subscribed before the manager starts: a rejection at the first check reaches the user
	// once the notification sink below exists.
	ruleRejections := eventBus.Subscribe(secports.KindRuleUpdateRejected)
	ruleManager, err := startRules(ctx, logger, db, settings, egress, redact, activeRules, eventBus, metrics)
	if err != nil {
		logger.Error("cannot start the rule manager", slog.Any("error", err))
		return exitCantCreate
	}

	models, err := newGateway(ctx, logger, modelStore, egress, usage, modelTracer{telemetry}, redact)
	if err != nil {
		logger.Error("cannot build the model gateway", slog.Any("error", err))
		return exitCantCreate
	}
	providers.observe(models.configure)

	// The skill store (T-F1-34), its leftovers settled before anyone can install, and before
	// the agent runtime, whose turns list its catalog and whose skill_load reads it (T-F1-36).
	skillStore, err := newSkills(ctx, db, eventBus, logger)
	if err != nil {
		logger.Error("cannot build the skill store", slog.Any("error", err))
		return exitCantCreate
	}

	// The worktree engine (T-F2-02), its missing worktrees marked before the runtime can run a
	// turn in one (REQ-WT-006).
	worktreeEngine := newWorktrees(ctx, db, time.Duration(settings.WorktreeCommitTimeoutS)*time.Second, logger)

	// The host of `[[acp.agents]]` (T-F2-07). It is closed after the runtime, which cancels
	// every turn first, so a hosted agent's turn ends `cancelled` rather than killed.
	var runtimeRef atomic.Pointer[agents.Runtime]
	acpHost, err := newACP(settings.ACP, filepath.Dir(socket), providers, sessionService, func(threadID string) bool {
		rt := runtimeRef.Load()
		return rt != nil && rt.AwaitingApproval(threadID)
	}, logger)
	if err != nil {
		logger.Error("cannot build the ACP host", slog.Any("error", err))
		return exitCantCreate
	}
	defer acpHost.Shutdown()

	// The agent runtime (T-F1-13). Its turns run on the daemon's context; closing it waits
	// for each to record how it ended.
	runtime, tools, err := newRuntime(ctx, agentDeps{
		agents:    runtimeAgents{host: acpHost},
		worktrees: runtimeWorktrees{svc: worktreeEngine},
		logger:    logger, db: db, bus: eventBus, gateway: models, egress: egress,
		terminal: sessionService, keys: sessionService, blocks: blockReader, metrics: metrics, tracer: agentTracer{telemetry},
		maxCost: providers.MaxCostMicroUSD, redact: redact, policy: activeRules.Policy, skills: skillStore,
		stallLocal:  time.Duration(settings.Stall.LocalS) * time.Second,
		stallRemote: time.Duration(settings.Stall.RemoteS) * time.Second,
		maxTurns:    settings.MaxConcurrentTurns,
	})
	if err != nil {
		logger.Error("cannot build the agent runtime", slog.Any("error", err))
		return exitCantCreate
	}
	defer runtime.Close()
	runtimeRef.Store(runtime)
	// Output from a thread's own pane keeps its turn from being marked stalled (T-F1-31).
	go followThreadOutput(ctx, eventBus.Subscribe(sessports.KindBlockStarted), eventBus.Subscribe(sessports.KindSessionOutput), runtime)

	// policy.explain (T-F1-28) reads the same threads and rules a turn decides with.
	explainer, err := newExplainer(db, tools, activeRules.Policy, runtimeWorktrees{svc: worktreeEngine})
	if err != nil {
		logger.Error("cannot build policy.explain", slog.Any("error", err))
		return exitCantCreate
	}

	// The MCP client (T-F1-17): every configured server is connected in the background and
	// its tools join the registry the runtime offers from.
	mcpClient, err := newMCP(ctx, logger, db, eventBus, egress, tools, providers, redact)
	if err != nil {
		logger.Error("cannot build the MCP client", slog.Any("error", err))
		return exitCantCreate
	}
	defer mcpClient.Close()

	// The wait engine (T-F1-23): thread.wait, block.wait_output and thread.send's wait.
	waitEngine, err := newWaits(eventBus, runtime, sessionService, blockReader)
	if err != nil {
		logger.Error("cannot build the wait engine", slog.Any("error", err))
		return exitCantCreate
	}
	gauges.waits.Store(waitEngine)

	// Active AI (T-F2-22): a one-line suggestion from a local model when a user's command
	// fails. Off unless `[active_ai] enabled`; subscribed before the socket serves.
	suggestions, err := newActiveAI(settings.ActiveAI, eventBus, models.router, blockReader, logger)
	if err != nil {
		logger.Error("cannot build Active AI", slog.Any("error", err))
		return exitCantCreate
	}
	if suggestions != nil {
		// Its own context, as the retention job's: Serve can return without ctx ending.
		suggestCtx, stopSuggestions := context.WithCancel(ctx)
		suggestionsDone := make(chan struct{})
		go func() {
			defer close(suggestionsDone)
			suggestions.Run(suggestCtx)
		}()
		defer func() {
			stopSuggestions()
			<-suggestionsDone
		}()
	}

	// The notification sink (T-F1-27). Its deliverer is the API server, built below.
	notifications, err := notify.New(notify.Config{Enabled: settings.NotificationsEnabled})
	if err != nil {
		logger.Error("cannot build the notification sink", slog.Any("error", err))
		return exitCantCreate
	}

	go notifyRuleRejections(ctx, ruleRejections, notifications, logger)

	// The integration surface (T-F1-25): external state reports and `pane.state_changed`.
	paneStates, err := newIntegrations(ctx, db, eventBus, workspaceService,
		blockedNotifier{sink: notifications, logger: logger}, metrics.ReportRateLimited, logger)
	if err != nil {
		logger.Error("cannot build the integration surface", slog.Any("error", err))
		return exitCantCreate
	}

	// The plugin store (T-F2-60), whose servers and skills go through the MCP client and the
	// skill store, and whose detection manifests the detector reads below.
	plugins, err := newPlugins(ctx, db, mcpClient, skillStore, eventBus, logger)
	if err != nil {
		logger.Error("cannot build the plugin store", slog.Any("error", err))
		return exitCantCreate
	}
	pluginDetection, err := plugins.DetectionFiles(ctx)
	if err != nil {
		logger.Error("cannot list the plugins' detection manifests", slog.Any("error", err))
		return exitCantCreate
	}

	// Third-party agent detection (T-F2-10): a label beside the pane, never its state (DD-012).
	detector, err := startDetection(ctx, settingsPath, pluginDetection, agentLabels, sessionService, workspaceService, eventBus, logger)
	if err != nil {
		logger.Error("cannot build agent detection", slog.Any("error", err))
		return exitCantCreate
	}

	var inherited *net.UnixListener
	if took != nil {
		inherited = took.listener
	}
	// The daemon's own lifetime from here: a handoff ends it without a signal.
	ctx, stopDaemon := context.WithCancel(ctx)
	defer stopDaemon()
	// Remote daemons over ssh (T-F2-41), linked for this daemon's lifetime.
	// On Windows the module is not wired in, so remote.* answer NOT_IMPLEMENTED (decision 11);
	// the interface stays nil rather than holding a nil *Manager.
	var remoteHosts remoteports.Hosts
	if remoteHostsSupported {
		m, err := startRemotes(ctx, settings.Remote, filepath.Dir(socket), db, eventBus, logger)
		if err != nil {
			logger.Error("cannot start the remote hosts", slog.Any("error", err))
			return exitCantCreate
		}
		defer m.Close()
		remoteHosts = m
	}
	// A client inside an agent's terminal, or inside a hosted ACP agent: the API refuses it
	// what lands, drops or approves the agent's own work, and the handoff socket everything.
	insideAnAgent := agentPeer(func() []int {
		return append(sessionService.AgentShellPIDs(), acpHost.AgentPIDs()...)
	}, proctree.Ancestors, sessionOf(sessionService, acpHost))
	server, err := api.Listen(ctx, api.Config{
		SocketPath:    socket,
		Listener:      inherited,
		TokenPath:     tokenPath,
		DaemonVersion: buildVersion(),
		Status:        withMCP(withProviders(statusFromStore(db), providers), mcpClient),
		Configuration: providers,
		Models:        models,
		ModelRoutes:   models,
		Threads:       runtime,
		MCP:           mcpClient,
		Waits:         waitEngine,
		Sessions:      sessionService,
		Terminals:     sessionService,
		Blocks:        blockReader,
		Workspaces:    workspaceService,
		Integrations:  paneStates,
		Notifications: notifications,
		Agents:        detector,
		PolicyExplain: explainer,
		Rules:         ruleManager,
		Skills:        skillStore,
		Plugins:       plugins,
		// worktree.* (T-F2-03): the engine, and the runtime's word on when a thread's may change.
		Worktrees:       worktreeEngine,
		WorktreeThreads: runtime,
		// remote.* and the relay to remote hosts' daemons (T-F2-41).
		Remotes: remoteHosts,
		// A client inside an agent's terminal, or inside a hosted ACP agent, is refused what
		// lands, drops or approves the agent's own work (REQ-ORC-004).
		AgentPeer: insideAnAgent,
		Bus:       eventBus,
		Logger:    logger,
		// The first live setting: `limits.set` rewrites it in this file and applies it to
		// new connections without a restart (REQ-CLI-007, Tech Design §5.1).
		MaxMessageBytes: settings.MaxMessageBytes,
		SettingsPath:    settingsPath,
	})
	if err != nil {
		logger.Error("cannot open the socket", slog.Any("error", err))
		return exitCantCreate
	}
	gauges.server.Store(server)
	notifications.SetDeliverer(server)
	defer func() {
		if err := server.Close(); err != nil {
			logger.Error("cannot close the socket", slog.Any("error", err))
		}
	}()
	if took != nil {
		// The socket's path stays the old daemon's while it may resume.
		defer func() {
			if !committed {
				server.Abandon()
			}
		}()
		if handoffFailAt == stepStart {
			return exitTempFail
		}
		if err := took.confirm(); err != nil {
			logger.Error("the handoff failed; the running daemon keeps serving",
				slog.String("step", stepConfirm), slog.Any("error", err))
			committed = true // the sessions stay the old daemon's
			sessionService.DropAdopted()
			server.Abandon()
			took.close()
			return exitTempFail
		}
		committed = true
		//nolint:contextcheck // the sessions' drains outlive this call, as they do on a create
		sessionService.StartAdopted()
		logger.Info("took the running daemon's sessions over",
			slog.Int("sessions", len(took.handed)), slog.Duration("since_connect", time.Since(took.started)))
	}

	// Every daemon can be taken over in its turn (REQ-HND-001).
	handoffListener, err := handoff.Listen(filepath.Join(filepath.Dir(socket), handoff.SocketName))
	if err != nil {
		logger.Warn("no handoff socket; this daemon cannot be taken over", slog.Any("error", err))
	} else {
		g := &giver{
			server: server, runtime: runtime, sessions: sessionService, lock: lock, dbLock: dbLock,
			dbPath: *dbPath, logger: logger, agentPeer: insideAnAgent, manager: serviceManager(), handedOver: stopDaemon,
		}
		go g.serveHandoff(ctx, handoffListener)
	}

	// Forward module events to connected clients (API Spec §6).
	go server.Notify(ctx)

	// REQ-TERM-010's capture: every 10 s while the setting is on, and once more when the
	// context ends, which is the "on clean shutdown" half. A no-op when it is off.
	captureDone := make(chan struct{})
	go func() {
		defer close(captureDone)
		workspaceService.RunCapture(ctx)
	}()
	defer func() { <-captureDone }()

	// Data Model §4's maintenance job: once now, then daily.
	// Its own context, cancelled on the way out: Serve can return without ctx ending, and the
	// wait below must not outlive the daemon.
	retentionCtx, stopRetention := context.WithCancel(ctx)
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		runRetention(retentionCtx, db, storeRetention(settings.Retention), retentionInterval, logger)
	}()
	defer func() {
		stopRetention()
		<-retentionDone
	}()

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

// resolveSocketPath honours an explicit -socket flag and otherwise asks config for the
// platform default, which is the same answer `umb` gets.
func resolveSocketPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	return config.DefaultSocketPath()
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

// withProviders adds the providers' health and reasons to system.status (REQ-SEC-008).
func withProviders(status api.StatusFunc, providers *providerConfig) api.StatusFunc {
	return func(ctx context.Context) (api.StatusResult, error) {
		result, err := status(ctx)
		if err != nil {
			return result, err
		}
		result.Providers = providers.Statuses()
		return result, nil
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

// screenReader adapts the sessions module to the narrow view REQ-TERM-010's capture needs.
type screenReader struct {
	sessions sessports.Sessions
}

func (r screenReader) Screen(ctx context.Context, sessionID string) (wsports.Screen, error) {
	snapshot, err := r.sessions.Snapshot(ctx, sessionID)
	if err != nil {
		return wsports.Screen{}, err
	}
	// The cursor's row is the closest thing the snapshot has to "how many lines this is",
	// and Data Model §2.4d stores `rows` beside the screen so a reader knows what it got.
	return wsports.Screen{Data: snapshot.Data, Rows: int(snapshot.CursorY) + 1}, nil
}

// findUmb locates the `umb` every pane is told about as UMBRAL_BIN_PATH (REQ-INT-001): the one
// beside this binary first, because it is the same release as the daemon a hook will talk
// to, then PATH. Neither is fatal: the pane is told nothing, and a hook falls back to its own
// PATH.
func findUmb(logger *slog.Logger) string {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	bin := findUmbFrom(self, exec.LookPath)
	if bin == "" {
		logger.Warn("umb is neither beside umbrald nor on PATH; panes get no UMBRAL_BIN_PATH")
	}
	return bin
}

// findUmbFrom is findUmb's search, given this binary's path and a PATH lookup.
func findUmbFrom(self string, lookPath func(string) (string, error)) string {
	if self != "" {
		sibling := filepath.Join(filepath.Dir(self), "umb")
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return sibling
		}
	}
	if bin, err := lookPath("umb"); err == nil {
		if abs, err := filepath.Abs(bin); err == nil {
			return abs
		}
	}
	return ""
}
