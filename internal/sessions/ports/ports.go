// Package ports declares what the sessions module publishes to the rest of the daemon and
// what it needs from the outside world.
//
// Everything above the module, the api layer and later the agent runtime, depends on these
// interfaces rather than on the PTY or the emulator. That is what keeps libghostty and
// creack/pty confined to one directory each, and what makes the Q-01 risk, bindings with
// no stable API, containable.
package ports

import (
	"context"
	"io"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// Sessions is the module's inbound port: what a client can ask of it.
//
// The api layer holds this interface and never the concrete service, so that api cannot
// reach past it into the PTY.
type Sessions interface {
	// Create launches a shell in a PTY (REQ-TERM-001).
	Create(ctx context.Context, params domain.CreateParams) (domain.Session, error)
	// List reports every session the daemon knows, alive or exited.
	List(ctx context.Context) ([]domain.Session, error)
	// Get reports one session, or domain.ErrNotFound.
	Get(ctx context.Context, id string) (domain.Session, error)
	// Input writes to the session's PTY on behalf of writer, refusing with
	// domain.ErrInputLocked when the lock belongs to someone else (REQ-TERM-008).
	Input(ctx context.Context, id string, data []byte, writer domain.InputOwner) error
	// Resize applies a new size to the PTY and the emulator (REQ-TERM-007).
	Resize(ctx context.Context, id string, size domain.Size) error
	// SetInputOwner hands the write lock to the agent or back to the human (DD-003).
	SetInputOwner(ctx context.Context, id string, owner domain.InputOwner) error
	// Close sends SIGHUP and, after a grace period, SIGKILL (API Spec §5.9).
	Close(ctx context.Context, id string) error
	// Snapshot renders the current screen as replayable VT (REQ-TERM-004).
	Snapshot(ctx context.Context, id string) ([]byte, error)
}

// Bootstrapper injects shell integration into a session's argv and environment
// (REQ-BLK-005).
//
// A shell it does not support is not an error the caller should propagate: the session
// starts without integration and REQ-BLK-003 marks it `integration: none`, which is the
// degraded mode DD-002 describes.
type Bootstrapper interface {
	Prepare(shellPath string) (args, env []string, cleanup func() error, err error)
}

// PTY is a pseudo-terminal the daemon owns.
type PTY interface {
	io.ReadWriteCloser
	// Resize tells the kernel the window changed, which is what makes the shell
	// redraw and what SIGWINCH-aware programs react to.
	Resize(size domain.Size) error
	// Wait blocks until the child exits and reports its exit code.
	Wait() (exitCode int, err error)
	// Signal sends a signal to the child's process group.
	Signal(sig SignalKind) error
}

// SignalKind is the small set of signals the sessions module sends, named here so the
// domain and the service never import syscall.
type SignalKind int

const (
	// SignalHangup asks the shell to exit, the polite half of session.close.
	SignalHangup SignalKind = iota
	// SignalKill is the deadline enforcement three seconds later.
	SignalKill
)

// PTYFactory opens a PTY running the given command. It is a function rather than an
// interface because there is exactly one thing to do with it.
type PTYFactory func(spec PTYSpec) (PTY, error)

// PTYSpec is everything needed to launch a shell under a PTY.
type PTYSpec struct {
	// Path is the shell executable.
	Path string
	// Args are the arguments after argv[0], including any shell-integration bootstrap.
	Args []string
	// Env is the complete environment for the child.
	Env []string
	// Dir is the working directory.
	Dir string
	// Size is the initial window size.
	Size domain.Size
}

// Emulator is the VT state of one session. The daemon owns it and clients render from the
// bytes it produces (DD-001).
type Emulator interface {
	// Write feeds PTY output into the emulator.
	Write(p []byte) (int, error)
	// Resize changes the screen dimensions.
	Resize(size domain.Size) error
	// Snapshot renders the screen as replayable VT (REQ-TERM-004).
	Snapshot() ([]byte, error)
	// PlainText renders the screen without escape sequences (REQ-BLK-007).
	PlainText() (string, error)
	// Close releases the emulator.
	Close() error
}

// EmulatorFactory builds an emulator for a given size.
type EmulatorFactory func(size domain.Size) (Emulator, error)
