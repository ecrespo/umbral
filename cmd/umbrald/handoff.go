package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/handoff"
	"github.com/ecrespo/umbral/internal/sessions"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// A live handoff (REQ-HND-001, REQ-HND-002, delta `2026-10-pty-handoff`): `umbrald --takeover`
// asks the daemon serving its runtime directory for its sessions over `handoff.sock`, and that
// daemon exits once the new one has them. The steps are named, and a failure names its step.
const (
	stepConnect  = "connect"
	stepHello    = "hello"
	stepQuiesce  = "quiesce"
	stepTransfer = "transfer"
	stepAdopt    = "adopt"
	stepStart    = "start"
	stepConfirm  = "confirm"
)

// The frames' ops. A hello is the step's own name; the rest close the exchange.
const (
	opHello    = stepHello
	opReady    = "ready"
	opReleased = "released"
	opError    = "error"
)

// handoffTimeout bounds each wait for the other daemon's next frame. Past it the old daemon
// resumes and the new one gives up.
const handoffTimeout = 30 * time.Second

// handoffFailAt makes the new daemon fail at the named step (decision 8). It is read from the
// environment variable handoffFailEnv names, and that name is empty in every release build:
// only the test build sets it, with -ldflags -X, so no environment can inject a failure into a
// daemon a user runs.
var (
	handoffFailEnv string
	handoffFailAt  = func() string {
		if handoffFailEnv == "" {
			return ""
		}
		return os.Getenv(handoffFailEnv)
	}()
)

// errInjected is the failure handoffFailAt asks for.
var errInjected = errors.New("failure injected by the test build")

// handoffFrame is every frame of the handoff but a session's.
type handoffFrame struct {
	Op string `json:"op"`
	// Version and Protocol are the hello's: the daemon's build and its API protocol major.
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	// Step and Error are a refusal's or a failure's.
	Step  string `json:"step,omitempty"`
	Error string `json:"error,omitempty"`
	// Sessions and Database are the transfer's header: how many session frames follow, and
	// the database the old daemon holds open, which the new one must open too.
	Sessions int    `json:"sessions,omitempty"`
	Database string `json:"database,omitempty"`
}

// handoffSession is one session's frame; its PTY master travels beside it.
type handoffSession struct {
	Handed sessdomain.Handed `json:"handed"`
}

// stepError is a handoff failure and the step it happened in.
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return "handoff failed at " + e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func failed(step string, err error) error { return &stepError{step: step, err: err} }

// ---- the old daemon ---------------------------------------------------------------------

// giver is what the serving daemon hands over.
type giver struct {
	server   *api.Server
	runtime  interface{ Busy() bool }
	sessions *sessions.Service
	lock     *config.InstanceLock
	dbLock   *config.InstanceLock
	dbPath   string
	logger   *slog.Logger
	// agentPeer is the API's own check: a process inside an agent's terminal, or one whose pid
	// the kernel did not give, is refused every session's master as it is the API's methods
	// that land or approve the agent's work (REQ-ORC-004).
	agentPeer func(pid int) bool
	// manager names the service manager running this daemon, "" when none does. Under one a
	// handoff is refused: this daemon's exit ends its unit, and a stop kills what is left in
	// the unit's processes — every shell the new daemon adopted (delta `2026-10-service-units`).
	manager string
	// handedOver is called once the new daemon has everything: the old one's serve returns.
	handedOver func()
}

// serveHandoff answers handoff requests one at a time until ctx ends or one succeeds.
func (g *giver) serveHandoff(ctx context.Context, ln *handoff.Listener) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		ok := g.give(ctx, c)
		_ = c.Close()
		if ok {
			ln.Abandon()
			g.handedOver()
			return
		}
	}
}

// give runs the old daemon's half. Before `ready` any failure resumes everything as it was
// (REQ-HND-002); once `ready` has arrived the sessions are the new daemon's.
func (g *giver) give(ctx context.Context, c *handoff.Conn) bool {
	deadline := func() { _ = c.SetDeadline(time.Now().Add(handoffTimeout)) }
	fail := func(step string, err error) bool {
		g.logger.Warn("a handoff failed; this daemon keeps serving",
			slog.String("step", step), slog.Any("error", err))
		_ = c.Send(handoffFrame{Op: opError, Step: step, Error: err.Error()})
		return false
	}

	if g.manager == "systemd" {
		return fail(stepHello, errors.New("refused: this daemon runs under systemd, which would end the shells it hands over; run `systemctl --user restart umbrald` instead"))
	}
	if pid := c.PeerPID(); g.agentPeer != nil && (pid <= 0 || g.agentPeer(pid)) {
		return fail(stepHello, errors.New("refused: the peer runs inside an agent's terminal"))
	}
	deadline()
	var hello handoffFrame
	if _, err := c.Recv(&hello); err != nil {
		return fail(stepHello, err)
	}
	if err := c.Send(handoffFrame{Op: opHello, Version: buildVersion(), Protocol: api.ProtocolVersion}); err != nil {
		return fail(stepHello, err)
	}
	if hello.Protocol != api.ProtocolVersion {
		return fail(stepHello, fmt.Errorf("protocol %d cannot take over protocol %d", hello.Protocol, api.ProtocolVersion))
	}
	g.logger.Info("a daemon asks to take this one's sessions over", slog.String("version", hello.Version))

	var ask handoffFrame
	if _, err := c.Recv(&ask); err != nil || ask.Op != stepQuiesce {
		return fail(stepQuiesce, orUnexpected(err, ask))
	}
	// A busy daemon refuses before it pauses anything, so its clients stay connected. The check
	// is made again once accepting has stopped, for a turn a request started meanwhile.
	if g.runtime.Busy() {
		return fail(stepQuiesce, errors.New("busy: a turn is running or queued"))
	}
	quiesced := time.Now()
	if err := g.server.Suspend(ctx); err != nil {
		return fail(stepQuiesce, err)
	}
	if g.runtime.Busy() {
		g.server.Resume()
		return fail(stepQuiesce, errors.New("busy: a turn is running or queued"))
	}
	handed, masters, err := g.sessions.Freeze(ctx)
	if err != nil {
		g.server.Resume()
		return fail(stepQuiesce, err)
	}
	resume := func(step string, err error) bool {
		g.sessions.Thaw()
		g.server.Resume()
		return fail(step, err)
	}

	listener, err := g.server.ListenerFile()
	if err != nil {
		return resume(stepTransfer, err)
	}
	defer func() { _ = listener.Close() }()
	deadline()
	if err := c.Send(handoffFrame{Op: stepTransfer, Sessions: len(handed), Database: g.dbPath},
		listener, g.lock.File(), g.dbLock.File()); err != nil {
		return resume(stepTransfer, err)
	}
	for i, h := range handed {
		if err := c.Send(handoffSession{Handed: h}, masters[i]); err != nil {
			return resume(stepTransfer, err)
		}
	}

	deadline()
	var ready handoffFrame
	if _, err := c.Recv(&ready); err != nil || ready.Op != opReady {
		step := stepConfirm
		if ready.Step != "" {
			step = ready.Step
		}
		return resume(step, orUnexpected(err, ready))
	}

	// The new daemon has built everything and waits only for this daemon to let go: from here
	// nothing resumes.
	g.sessions.ReleaseHanded()
	g.server.Abandon()
	if err := c.Send(handoffFrame{Op: opReleased}); err != nil {
		g.logger.Error("the sessions are handed over, but the new daemon was not told",
			slog.Any("error", err))
	}
	g.logger.Info("sessions handed over to a new daemon; exiting",
		slog.Int("sessions", len(handed)), slog.Duration("paused", time.Since(quiesced)))
	return true
}

// orUnexpected is err, or the frame the other side sent instead of the one expected.
func orUnexpected(err error, got handoffFrame) error {
	if err != nil {
		return err
	}
	if got.Op == opError {
		return fmt.Errorf("the other daemon failed at %s: %s", got.Step, got.Error)
	}
	return fmt.Errorf("unexpected frame %q", got.Op)
}

// ---- the new daemon ---------------------------------------------------------------------

// taken is what the new daemon received: everything it needs to serve the old one's sessions.
type taken struct {
	conn     *handoff.Conn
	listener *net.UnixListener
	lock     *config.InstanceLock
	dbLock   *config.InstanceLock
	dbPath   string
	handed   []sessdomain.Handed
	masters  []*os.File
	started  time.Time
}

// takeOver runs the new daemon's half up to the transfer: after it the old daemon is paused,
// and this one holds descriptors for its socket, its locks and its sessions.
func takeOver(ctx context.Context, socketDir string, logger *slog.Logger) (*taken, error) {
	if handoffFailAt == stepConnect {
		return nil, failed(stepConnect, errInjected)
	}
	dial, cancel := context.WithTimeout(ctx, handoffTimeout)
	defer cancel()
	c, err := handoff.Dial(dial, filepath.Join(socketDir, handoff.SocketName))
	if err != nil {
		return nil, failed(stepConnect, err)
	}
	t := &taken{conn: c, started: time.Now()}
	fail := func(step string, err error) (*taken, error) {
		_ = c.Send(handoffFrame{Op: opError, Step: step, Error: err.Error()})
		t.close()
		return nil, failed(step, err)
	}
	_ = c.SetDeadline(time.Now().Add(handoffTimeout))

	if err := c.Send(handoffFrame{Op: opHello, Version: buildVersion(), Protocol: api.ProtocolVersion}); err != nil {
		return fail(stepHello, err)
	}
	var hello handoffFrame
	if _, err := c.Recv(&hello); err != nil || hello.Op != opHello {
		return fail(stepHello, orUnexpected(err, hello))
	}
	if hello.Protocol != api.ProtocolVersion {
		return fail(stepHello, fmt.Errorf("protocol %d cannot take over protocol %d", api.ProtocolVersion, hello.Protocol))
	}
	if handoffFailAt == stepHello {
		return fail(stepHello, errInjected)
	}
	logger.Info("taking over a running daemon", slog.String("its_version", hello.Version))

	if err := c.Send(handoffFrame{Op: stepQuiesce}); err != nil {
		return fail(stepQuiesce, err)
	}
	var header handoffFrame
	files, err := c.Recv(&header)
	if err != nil || header.Op != stepTransfer {
		return fail(stepQuiesce, orUnexpected(err, header))
	}
	if handoffFailAt == stepQuiesce {
		closeFiles(files)
		return fail(stepQuiesce, errInjected)
	}
	if len(files) != 3 {
		closeFiles(files)
		return fail(stepTransfer, fmt.Errorf("%d descriptors with the transfer, want 3", len(files)))
	}
	ln, err := net.FileListener(files[0])
	_ = files[0].Close()
	if err != nil {
		closeFiles(files[1:])
		return fail(stepTransfer, err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		closeFiles(files[1:])
		return fail(stepTransfer, errors.New("the socket passed is not a Unix listener"))
	}
	// The inherited socket's path belongs to the old daemon until it lets go.
	ul.SetUnlinkOnClose(false)
	t.listener, t.lock, t.dbLock, t.dbPath = ul, config.AdoptLock(files[1]), config.AdoptLock(files[2]), header.Database

	for range header.Sessions {
		var frame handoffSession
		masters, err := c.Recv(&frame)
		if err != nil {
			return fail(stepTransfer, err)
		}
		if len(masters) != 1 {
			closeFiles(masters)
			return fail(stepTransfer, fmt.Errorf("session %s came with %d descriptors", frame.Handed.Session.ID, len(masters)))
		}
		t.handed = append(t.handed, frame.Handed)
		t.masters = append(t.masters, masters[0])
	}
	if handoffFailAt == stepTransfer {
		return fail(stepTransfer, errInjected)
	}
	return t, nil
}

// confirm tells the old daemon this one is ready and waits for it to let go. `released`
// commits, and so does the connection closing without an answer: once it has read `ready`
// the old daemon releases the sessions before it answers and exits even when the answer is
// lost, so a daemon that gave up here would leave every shell to the hang-up. An `error`
// frame, a timeout or a failed send of `ready` mean the old daemon still has its sessions.
func (t *taken) confirm() error {
	if handoffFailAt == stepConfirm {
		return t.abort(stepConfirm, errInjected)
	}
	_ = t.conn.SetDeadline(time.Now().Add(handoffTimeout))
	if err := t.conn.Send(handoffFrame{Op: opReady}); err != nil {
		return failed(stepConfirm, err)
	}
	var released handoffFrame
	_, err := t.conn.Recv(&released)
	if errors.Is(err, io.EOF) {
		err, released.Op = nil, opReleased
	}
	if err != nil || released.Op != opReleased {
		return failed(stepConfirm, orUnexpected(err, released))
	}
	_ = t.conn.Close()
	return nil
}

// abort tells the old daemon this one failed at step, so it resumes at once rather than at its
// timeout, and closes what was received.
func (t *taken) abort(step string, err error) error {
	_ = t.conn.Send(handoffFrame{Op: opError, Step: step, Error: err.Error()})
	return failed(step, err)
}

// close releases everything received and the connection, which the old daemon reads as a
// failure if it is still waiting.
func (t *taken) close() {
	if t.listener != nil {
		_ = t.listener.Close()
	}
	closeFiles(t.masters)
	_ = t.conn.Close()
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// handoffStep is the step a handoff error names.
func handoffStep(err error) string {
	var se *stepError
	if errors.As(err, &se) {
		return se.step
	}
	return stepStart
}

// serviceManager names the service manager that runs this process as its unit: systemd sets
// SYSTEMD_EXEC_PID to the pid it executed. Every process a unit or scope starts inherits it,
// and INVOCATION_ID, so only a pid equal to this one counts: a daemon `umb` autostarted from a
// terminal under some unit is not the unit's.
func serviceManager() string {
	if os.Getenv("SYSTEMD_EXEC_PID") == strconv.Itoa(os.Getpid()) {
		return "systemd"
	}
	return ""
}
