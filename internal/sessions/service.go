// Package sessions is the terminal module's application core: it owns the live PTYs, their
// emulators and the rules about who may write to them.
//
// It sits behind ports.Sessions, so the api layer and later the agent runtime depend on the
// interface rather than on this type. The PTY, the emulator and the shell bootstrap all
// arrive as injected factories, which is what lets the whole state machine be tested with
// fakes and only the adapters be tested against a real shell.
package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// CloseGrace is how long session.close waits after SIGHUP before SIGKILL (API Spec §5.9).
const CloseGrace = 3 * time.Second

// readBufferSize is the PTY read chunk. 32 KiB matches the batching threshold T-F0-06 uses,
// so a burst of output crosses the boundary in whole reads.
const readBufferSize = 32 << 10

// Config wires the service. Every field is required except Logger and Bootstrap.
type Config struct {
	Store     *store.Store
	Bus       *bus.Bus
	NewPTY    ports.PTYFactory
	NewEmu    ports.EmulatorFactory
	Bootstrap ports.Bootstrapper
	Logger    *slog.Logger
	// Blocks and NewScanner turn PTY output into blocks (T-F0-09). Leaving either nil
	// runs the session without block recording, which is what the older tests and the
	// spike binaries do.
	Blocks     ports.BlockStore
	NewScanner ports.ScannerFactory
	// Host is stored on every block so an exported history says where it ran. It
	// defaults to the machine's hostname.
	Host string
	// DefaultShell and DefaultCWD fill in the optional create parameters (API Spec §5.3).
	DefaultShell string
	DefaultCWD   string
	// SocketPath and BinPath are the daemon's socket and the `umb` binary, which every
	// session is told as UMBRAL_SOCKET_PATH and UMBRAL_BIN_PATH (REQ-INT-001) so an
	// integration's hook can call back without guessing. An empty one is left out.
	SocketPath string
	BinPath    string
	// AgentInterruptEvery is how often a cancelled agent command's shell is interrupted again
	// while it holds the PTY in silence (stopAgent). Zero is agentInterruptEvery; tests shorten
	// it to make the race between a late interrupt and the next command frequent.
	AgentInterruptEvery time.Duration
	// AdoptPTY and ResumeScanner rebuild the sessions a handoff carries (REQ-HND-001); a
	// daemon without them cannot take sessions over.
	AdoptPTY      ports.PTYAdopter
	ResumeScanner ports.ScannerResumer
}

// Service implements ports.Sessions.
type Service struct {
	// threadMu serialises finding or creating a thread's PTY (REQ-AGT-003).
	threadMu sync.Mutex
	cfg      Config

	mu   sync.RWMutex
	live map[string]*liveSession
	// frozen are the sessions a handoff paused, and adopted the ones it brought in that do
	// not read yet (handoff.go). Guarded by mu.
	frozen  []*liveSession
	adopted []*liveSession
}

// recentChunk is one read of output and its sequence number.
type recentChunk struct {
	seq  uint64
	data []byte
}

// maxRecentBytes is how much output a thread's terminal keeps for a Fresh wait: more than one
// screen of a full-screen program's redraw.
const maxRecentBytes = 256 << 10

// keepRecent adds a chunk to recent and drops the oldest past maxRecentBytes. Only a thread's
// own terminal keeps any: terminal_interact waits on no other session, and a user's pane would
// hold 256 KiB for nothing. Called with mu held.
func (l *liveSession) keepRecent(seq uint64, data []byte) {
	if l.session.OwnerThreadID == "" {
		return
	}
	l.recent = append(l.recent, recentChunk{seq: seq, data: data})
	l.recentBytes += len(data)
	for l.recentBytes > maxRecentBytes && len(l.recent) > 1 {
		l.recentBytes -= len(l.recent[0].data)
		l.recent = l.recent[1:]
	}
}

// outputAfter is the output after sequence number after that recent still holds, as text.
func (l *liveSession) outputAfter(after uint64) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var p domain.PlainText
	for _, c := range l.recent {
		if c.seq > after {
			p.Write(c.data)
		}
	}
	return p.String()
}

// liveSession is one running PTY and everything attached to it.
type liveSession struct {
	// mu guards the mutable fields below. The PTY and the emulator have their own
	// synchronisation.
	mu      sync.RWMutex
	session domain.Session

	pty ports.PTY
	emu ports.Emulator

	// threadCwd is the cwd a thread's PTY was started for (RunForThread), "" for any other
	// session. Guarded by mu.
	threadCwd string
	// terminal marks an ACP terminal's session (StartTerminal): the thread owns it, but it is
	// not the thread's PTY, which RunForThread must neither reuse nor close.
	terminal bool

	seq     uint64
	cleanup func() error
	// recent is the latest output, by sequence number, for a wait that reads what came after
	// some keys (WaitFor's Fresh); at most recentBytes of it. Guarded by mu.
	recent      []recentChunk
	recentBytes int

	// ctx is the session's own lifetime, used by every block write. A block outlives the
	// request that started its command, so the request's context is the wrong one.
	ctx    context.Context
	cancel context.CancelFunc

	// Block recording (T-F0-09). scanner and recorder are touched only by the drain
	// goroutine and by finish, which runs after it.
	scanner  ports.Scanner
	recorder *domain.Recorder
	// typeAtPrompt is text waiting to be placed on the shell's command line, once it has
	// one (REQ-TERM-011). It is cleared by the write, so it happens exactly once, and it
	// is guarded by mu because the drain goroutine and the grace timer both reach it.
	typeAtPrompt []byte
	// pendingRaw buffers output until it is worth a chunk; chunkSeq orders the chunks
	// within the open block.
	pendingRaw   []byte
	chunkSeq     int64
	chunkBlockID string

	integrationTimer *time.Timer

	// snapshotMu serialises a snapshot against the drain goroutine's write-then-count, so
	// the screen and the sequence number describe the same instant.
	snapshotMu sync.Mutex

	done     chan struct{}
	doneOnce sync.Once

	// agent is the command the agent is waiting on in this session (REQ-AGT-003), guarded by
	// mu; nil when none is.
	agent *agentRun
	// interrupted is set when stopAgent has interrupted the shell, and cleared by the next
	// agent command, which then types an empty line first (RunForThread).
	interrupted atomic.Bool

	// inputMu is held across every write's lock check and the write itself, and by a change of
	// the input's owner, so a take-over is never passed by a write that checked before it
	// (REQ-FTU-002).
	inputMu sync.Mutex
	// blockOpen is a block open on this session, whoever started it; guarded by mu.
	blockOpen bool
	// promptReady is the shell's prompt drawn and waiting for input (OSC 133;B) since the last
	// command started; guarded by mu.
	promptReady bool
	// prompts counts the prompts drawn, so a write can wait for the next one; guarded by mu.
	prompts uint64
	// interactLine is what terminal_interact typed at the prompt and has not entered yet
	// (REQ-FTU-003); guarded by mu.
	interactLine string
	// programLine is what terminal_interact typed to the program in the foreground since its
	// last enter or ctrl+c, as a program-input target; it is dropped when the block closes
	// (REQ-FTU-003). Guarded by mu.
	programLine string
	// takenOver is a thread's PTY the user has taken over (REQ-FTU-002), and takenBy the
	// connection that did, whose close hands it back; guarded by mu.
	takenOver bool
	takenBy   string
	// discardLine is a hand-back: the agent's next write at the prompt drops whatever the
	// user left on its line first (decision 6). Guarded by mu.
	discardLine bool
	// gate is a handoff's pause of this session's drain, nil when none is (handoff.go);
	// pendingVerdict is an adopted session whose integration verdict was still waiting.
	// Guarded by mu.
	gate           *gate
	pendingVerdict bool
}

// agentRun is one command the agent typed and is waiting for. claimed turns true once the
// drain goroutine has told the recorder the next block is the agent's; blockID is that block
// once it opens; the closed block arrives on done.
type agentRun struct {
	threadID string
	// via is what the command comes through, which its block records.
	via domain.BlockVia
	// out, when set, receives a copy of the block's raw output as it arrives.
	out func([]byte)
	// typed, when set, receives whether the command was handed to the shell: nil once its line
	// is written, or why it was not.
	typed   chan error
	claimed bool
	blockID string
	done    chan domain.AgentRun
	// unsaved is set when the block's row failed to be written or closed.
	unsaved bool
	// abandoned is a cancelled run whose block did not close in time. It keeps the PTY busy
	// until that block closes or the shell shows its next prompt, so its late block cannot
	// be taken for the next run's.
	abandoned bool
	// interactive is a line terminal_interact submitted: nobody waits for its block, and keys
	// may be sent to the program it started while it runs (REQ-FTU-001).
	interactive bool
	// cancelled is a run whose caller gave up. Its command is signalled once the shell has
	// started it (its block is open), never before: an interrupt that reaches a shell still
	// reading the line is lost, and the command then runs unstopped.
	cancelled bool
}

// New builds a service. It does not start anything: sessions come into being through Create.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("sessions: a Store is required")
	case cfg.Bus == nil:
		return nil, errors.New("sessions: a Bus is required")
	case cfg.NewPTY == nil:
		return nil, errors.New("sessions: a PTY factory is required")
	case cfg.NewEmu == nil:
		return nil, errors.New("sessions: an Emulator factory is required")
	}
	if cfg.AgentInterruptEvery <= 0 {
		cfg.AgentInterruptEvery = agentInterruptEvery
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.DefaultShell == "" {
		cfg.DefaultShell = defaultShell()
	}
	if cfg.DefaultCWD == "" {
		cfg.DefaultCWD, _ = os.UserHomeDir()
	}
	if cfg.Host == "" {
		cfg.Host, _ = os.Hostname()
	}
	return &Service{cfg: cfg, live: make(map[string]*liveSession)}, nil
}

// Create launches a shell in a PTY and starts draining it (REQ-TERM-001).
//
// The row is written before the PTY starts producing, so a client that sees the session id
// can always look the session up: persist before notifying (DD-007).
func (s *Service) Create(ctx context.Context, params domain.CreateParams) (domain.Session, error) {
	if params.Shell == "" {
		params.Shell = s.cfg.DefaultShell
	}
	if params.CWD == "" {
		params.CWD = s.cfg.DefaultCWD
	}
	if err := params.Validate(); err != nil {
		return domain.Session{}, err
	}

	// The id is chosen before the launch because the child is told it (REQ-INT-001).
	id := store.NewID(store.PrefixSession)
	program, args, env, cleanup, err := s.launch(id, params)
	if err != nil {
		return domain.Session{}, err
	}

	// The emulator answers device queries, and its answers have to reach the PTY that
	// does not exist yet. The target is handed over as soon as it does.
	replies := &replyTarget{logger: s.cfg.Logger}
	emu, err := s.cfg.NewEmu(params.Size, replies.write)
	if err != nil {
		runCleanup(cleanup)
		return domain.Session{}, err
	}

	// The previous run's screen, before the shell writes a byte (REQ-TERM-010). Into the
	// emulator only: these are bytes the terminal printed, and putting them on the PTY
	// would feed a program's old output back to it as input.
	//
	// A failure here is logged and not fatal. The screen is a convenience the user opted
	// into; a pane that comes back blank is a disappointment, a pane that fails to start
	// because its history could not be replayed is a broken terminal.
	if len(params.ReplayScreen) > 0 {
		if _, err := emu.Write(params.ReplayScreen); err != nil {
			s.cfg.Logger.Warn("could not replay the pane's stored screen",
				slog.Int("bytes", len(params.ReplayScreen)), slog.String("error", err.Error()))
		}
	}

	pty, err := s.cfg.NewPTY(ports.PTYSpec{
		Path: program, Args: args, Env: env, Dir: params.CWD, Size: params.Size,
	})
	if err != nil {
		_ = emu.Close()
		runCleanup(cleanup)
		return domain.Session{}, err
	}

	session := domain.Session{
		ID: id,
		// The program actually running, which for a pane with a command is that command
		// and not the shell nobody started. `session.list` is how a client sees it.
		Shell:       program,
		CWD:         params.CWD,
		Size:        params.Size,
		State:       domain.StateAlive,
		Integration: domain.IntegrationPending,
		InputOwner:  domain.InputOwnerHuman,
		CreatedAt:   time.Now(),
	}
	if params.OwnerThreadID != "" {
		session.OwnerThreadID, session.InputOwner = params.OwnerThreadID, domain.InputOwnerAgent
	}

	if err := s.persistCreate(ctx, session); err != nil {
		_ = pty.Close()
		_ = emu.Close()
		runCleanup(cleanup)
		return domain.Session{}, err
	}

	replies.attach(pty, session.ID)

	sessionCtx, cancel := blockContext()
	live := &liveSession{
		session: session, pty: pty, emu: emu, cleanup: cleanup, done: make(chan struct{}),
		ctx: sessionCtx, cancel: cancel,
	}
	if s.cfg.Blocks != nil && s.cfg.NewScanner != nil {
		live.scanner = s.cfg.NewScanner()
		live.recorder = domain.NewRecorder(domain.RecorderConfig{
			SessionID: session.ID,
			Host:      s.cfg.Host,
			NewID:     func() string { return store.NewID(store.PrefixBlock) },
		})
		s.startIntegrationTimer(live)
	}

	s.mu.Lock()
	s.live[session.ID] = live
	s.mu.Unlock()

	// Armed before the drain goroutine starts (REQ-TERM-011). The drain is what reads the
	// OSC 133 prompt marker that delivers the text, so arming afterwards races it: a shell
	// that reaches its first prompt quickly can have that marker consumed by a drain whose
	// `typeAtPrompt` is still empty, and the delivery then falls back to the two-second
	// grace timer. Nothing is lost either way — the command is never run — but the user
	// waits for no reason, and the ordering that cannot lose the marker costs nothing.
	s.armPendingInput(live, params.TypeAtPrompt)

	// The session outlives the request that created it, so the drain goroutine gets a
	// background context. REQ-TERM-003 is exactly this: the PTY survives its clients, and
	// passing the create call's context down would end the session when that call returns.
	//nolint:contextcheck // deliberate: the session's lifetime is not the request's
	go s.drain(live)

	return session, nil
}

// List reports every session the daemon knows, live or historical.
func (s *Service) List(ctx context.Context) ([]domain.Session, error) {
	rows, err := s.cfg.Store.DB().QueryContext(ctx, `
		SELECT id, shell, cwd, cols, rows, state, integration, input_owner, owner_thread_id,
		       exit_code, created_at, exited_at
		FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	return out, nil
}

// ShellPID is the pid of a live session's shell, for third-party agent detection
// (REQ-XAG-001). A session that is not running, or not this daemon's, reports false.
func (s *Service) ShellPID(id string) (int, bool) {
	live := s.lookup(id)
	if live == nil || live.snapshotState().State == domain.StateExited {
		return 0, false
	}
	pid := live.pty.Pid()
	return pid, pid > 0
}

// AgentShellPIDs lists the shells of the live sessions a thread owns: the agent's terminals.
// The API refuses a client running inside one what an agent may not ask for its own work
// (REQ-ORC-004).
func (s *Service) AgentShellPIDs() []int {
	s.mu.RLock()
	lives := make([]*liveSession, 0, len(s.live))
	for _, live := range s.live {
		lives = append(lives, live)
	}
	s.mu.RUnlock()
	var out []int
	for _, live := range lives {
		session := live.snapshotState()
		// An exited shell still counts while its process tree is open: on Windows its Job
		// Object outlives it until the session is closed, holding what it left behind
		// (delta `2026-10-windows-port`, decision 4). Unix PTYs keep no such tree.
		tree, hasTree := live.pty.(interface{ TreeOpen() bool })
		if session.OwnerThreadID == "" || (session.State == domain.StateExited && (!hasTree || !tree.TreeOpen())) {
			continue
		}
		if pid := live.pty.Pid(); pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

// AgentTreeHolding is the pid of the agent-owned shell whose process tree holds pid, or 0.
// It is the agent's-terminal check where the platform keeps a shell's tree in an object of its
// own — a Job Object on Windows, which has no POSIX session — so a process the shell's children
// left behind is still known as the agent's (REQ-ORC-004, delta `2026-10-windows-port`).
func (s *Service) AgentTreeHolding(pid int) int {
	s.mu.RLock()
	lives := make([]*liveSession, 0, len(s.live))
	for _, live := range s.live {
		lives = append(lives, live)
	}
	s.mu.RUnlock()
	for _, live := range lives {
		// An exited shell's tree is asked too: its job outlives the shell until finish closes
		// it, and a process the shell left behind is the agent's until then. Holds answers
		// false once the job is closed, which also ended everything in it.
		if live.snapshotState().OwnerThreadID == "" {
			continue
		}
		if tree, ok := live.pty.(interface{ Holds(pid int) bool }); ok && tree.Holds(pid) {
			return live.pty.Pid()
		}
	}
	return 0
}

// Get reports one session. A live session is answered from memory, since its state is
// fresher there than in the row.
func (s *Service) Get(ctx context.Context, id string) (domain.Session, error) {
	if live := s.lookup(id); live != nil {
		return live.snapshotState(), nil
	}

	row := s.cfg.Store.DB().QueryRowContext(ctx, `
		SELECT id, shell, cwd, cols, rows, state, integration, input_owner, owner_thread_id,
		       exit_code, created_at, exited_at
		FROM sessions WHERE id = ?`, id)
	session, err := scanSession(row)
	if err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

// Input writes to the PTY on behalf of writer (REQ-TERM-008).
//
// The lock is checked before the write, never after: the point of INPUT_LOCKED is that the
// bytes never reach the PTY, not that the client is told afterwards.
func (s *Service) Input(_ context.Context, id string, data []byte, writer domain.InputOwner) error {
	if err := domain.ValidateInput(data); err != nil {
		return err
	}

	live := s.lookup(id)
	if live == nil {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}

	live.inputMu.Lock()
	defer live.inputMu.Unlock()
	return writeInput(live, data, writer)
}

// writeInput checks the lock and writes, with inputMu held.
func writeInput(live *liveSession, data []byte, writer domain.InputOwner) error {
	live.mu.RLock()
	session := live.session
	live.mu.RUnlock()

	if err := session.CanAcceptInputFrom(writer); err != nil {
		return err
	}
	if _, err := live.pty.Write(data); err != nil {
		return fmt.Errorf("sessions: write to %s: %w", session.ID, err)
	}
	return nil
}

// Resize applies the new size to the PTY and the emulator and announces it (REQ-TERM-007).
//
// The PTY goes first: it is the one that can fail, and resizing the emulator to a size the
// kernel rejected would leave the daemon's idea of the screen wrong.
func (s *Service) Resize(ctx context.Context, id string, size domain.Size) error {
	if err := size.Validate(); err != nil {
		return err
	}

	live := s.lookup(id)
	if live == nil {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	if live.snapshotState().State == domain.StateExited {
		return fmt.Errorf("%w: %s", domain.ErrExited, id)
	}

	if err := live.pty.Resize(size); err != nil {
		return err
	}
	if err := live.emu.Resize(size); err != nil {
		return err
	}

	live.mu.Lock()
	live.session.Size = size
	live.mu.Unlock()

	if _, err := s.cfg.Store.DB().ExecContext(ctx,
		"UPDATE sessions SET cols = ?, rows = ? WHERE id = ?", size.Cols, size.Rows, id); err != nil {
		return fmt.Errorf("sessions: persist the new size of %s: %w", id, err)
	}

	s.cfg.Bus.Publish(ports.SessionResized{SessionID: id, Size: size})
	return nil
}

// SetInputOwner hands the write lock over (DD-003).
func (s *Service) SetInputOwner(ctx context.Context, id string, owner domain.InputOwner) error {
	if owner != domain.InputOwnerHuman && owner != domain.InputOwnerAgent {
		return fmt.Errorf("%w: unknown input owner %q", domain.ErrValidation, owner)
	}

	live := s.lookup(id)
	if live == nil {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	live.inputMu.Lock()
	defer live.inputMu.Unlock()
	return s.setInputOwner(ctx, live, owner)
}

// setInputOwner records and announces the input's new owner, with inputMu held.
func (s *Service) setInputOwner(ctx context.Context, live *liveSession, owner domain.InputOwner) error {
	live.mu.Lock()
	live.session.InputOwner = owner
	id := live.session.ID
	live.mu.Unlock()

	if _, err := s.cfg.Store.DB().ExecContext(ctx,
		"UPDATE sessions SET input_owner = ? WHERE id = ?", string(owner), id); err != nil {
		return fmt.Errorf("sessions: persist the input owner of %s: %w", id, err)
	}

	s.cfg.Bus.Publish(ports.SessionInputOwner{SessionID: id, InputOwner: owner})
	return nil
}

// Snapshot renders the session's current screen with the sequence number it is current as
// of (REQ-TERM-004).
//
// The sequence number is read under the same lock that the drain goroutine increments, so
// the pair cannot drift: a snapshot taken while output is arriving is either before or
// after a chunk, never half of one. That is what lets `session.subscribe` promise the
// client a gapless stream starting at seq + 1.
func (s *Service) Snapshot(_ context.Context, id string) (ports.Snapshot, error) {
	live := s.lookup(id)
	if live == nil {
		return ports.Snapshot{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}

	live.snapshotMu.Lock()
	defer live.snapshotMu.Unlock()

	data, err := live.emu.Snapshot()
	if err != nil {
		return ports.Snapshot{}, err
	}
	cursorX, cursorY, err := live.emu.Cursor()
	if err != nil {
		return ports.Snapshot{}, err
	}

	live.mu.RLock()
	seq := live.seq
	live.mu.RUnlock()

	return ports.Snapshot{Data: data, CursorX: cursorX, CursorY: cursorY, Seq: seq}, nil
}

// ScreenText renders the session's screen as plain text, with the sequence number it is
// current as of and the line still being written at the cursor ("" when there is none), all
// read under the same lock as Snapshot: output after it is exactly what carries a higher
// sequence number (block.wait_output, REQ-AUT-003).
func (s *Service) ScreenText(_ context.Context, id string) (text string, seq uint64, open string, err error) {
	live := s.lookup(id)
	if live == nil {
		return "", 0, "", fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}

	live.snapshotMu.Lock()
	defer live.snapshotMu.Unlock()

	if text, err = live.emu.PlainText(); err != nil {
		return "", 0, "", err
	}
	if open, err = live.emu.OpenLine(); err != nil {
		return "", 0, "", err
	}
	live.mu.RLock()
	seq = live.seq
	live.mu.RUnlock()
	return text, seq, open, nil
}

// Close asks the shell to exit, then insists (API Spec §5.9).
func (s *Service) Close(_ context.Context, id string) error {
	live := s.lookup(id)
	if live == nil {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}

	if err := live.pty.Signal(ports.SignalHangup); err != nil {
		return err
	}

	select {
	case <-live.done:
		return nil
	case <-time.After(CloseGrace):
	}

	if err := live.pty.Signal(ports.SignalKill); err != nil {
		return err
	}
	return nil
}

// shutdownGrace bounds how long Shutdown waits for the hung-up shells to be recorded as
// exited. A process that ignores SIGHUP and keeps the PTY's other end open is left to
// store.Recover rather than holding the daemon's exit.
const shutdownGrace = 5 * time.Second

// Shutdown closes every live PTY and waits, up to shutdownGrace, until each session's exit is
// persisted. The daemon calls it on the way out, before the store closes, so no shell is
// orphaned and no exit is written to a closed database; a row the wait gives up on is
// repaired on the next start by store.Recover.
func (s *Service) Shutdown() {
	s.mu.Lock()
	live := make([]*liveSession, 0, len(s.live))
	for _, l := range s.live {
		live = append(live, l)
	}
	s.mu.Unlock()

	for _, l := range live {
		_ = l.pty.Signal(ports.SignalHangup)
		_ = l.pty.Close()
	}

	deadline := time.NewTimer(shutdownGrace)
	defer deadline.Stop()
	for i, l := range live {
		select {
		case <-l.done:
		case <-deadline.C:
			s.cfg.Logger.Warn("shutdown: sessions still exiting",
				slog.Int("sessions", len(live)-i), slog.Duration("waited", shutdownGrace))
			return
		}
	}
}

func (s *Service) lookup(id string) *liveSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.live[id]
}

func (l *liveSession) snapshotState() domain.Session {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.session
}

func runCleanup(cleanup func() error) {
	if cleanup != nil {
		_ = cleanup()
	}
}
