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
}

// Service implements ports.Sessions.
type Service struct {
	cfg Config

	mu   sync.RWMutex
	live map[string]*liveSession
}

// liveSession is one running PTY and everything attached to it.
type liveSession struct {
	// mu guards the mutable fields below. The PTY and the emulator have their own
	// synchronisation.
	mu      sync.RWMutex
	session domain.Session

	pty ports.PTY
	emu ports.Emulator

	seq     uint64
	cleanup func() error

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

	program, args, env, cleanup, err := s.launch(params)
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
		ID: store.NewID(store.PrefixSession),
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
		SELECT id, shell, cwd, cols, rows, state, integration, input_owner,
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

// Get reports one session. A live session is answered from memory, since its state is
// fresher there than in the row.
func (s *Service) Get(ctx context.Context, id string) (domain.Session, error) {
	if live := s.lookup(id); live != nil {
		return live.snapshotState(), nil
	}

	row := s.cfg.Store.DB().QueryRowContext(ctx, `
		SELECT id, shell, cwd, cols, rows, state, integration, input_owner,
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

	live.mu.RLock()
	session := live.session
	live.mu.RUnlock()

	if err := session.CanAcceptInputFrom(writer); err != nil {
		return err
	}
	if _, err := live.pty.Write(data); err != nil {
		return fmt.Errorf("sessions: write to %s: %w", id, err)
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

	live.mu.Lock()
	live.session.InputOwner = owner
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

// Shutdown closes every live PTY. The daemon calls it on the way out so no shell is
// orphaned; the rows are repaired on the next start by store.Recover.
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

func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}
