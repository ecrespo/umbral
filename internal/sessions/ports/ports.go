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
	// Snapshot renders the current screen as replayable VT (REQ-TERM-004), and reports
	// the cursor and the sequence number the snapshot is current as of.
	//
	// The sequence number is what makes the subscription gapless: the client is told the
	// snapshot already contains everything up to `seq`, and live delivery starts at
	// `seq + 1` (API Spec §5.5).
	Snapshot(ctx context.Context, id string) (Snapshot, error)
}

// Snapshot is a session's screen at a moment, with the sequence number that anchors the
// live stream to it.
type Snapshot struct {
	// Data is replayable VT (REQ-TERM-004).
	Data []byte
	// CursorX and CursorY are the cursor's cell.
	CursorX uint16
	CursorY uint16
	// Seq is the last output chunk already reflected in Data.
	Seq uint64
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
	// Cursor reports the cursor's cell, which `session.subscribe` returns alongside the
	// snapshot so a client can place its own cursor without parsing the VT it just got.
	Cursor() (x, y uint16, err error)
	// Close releases the emulator.
	Close() error
}

// ReplyFunc carries a terminal's answer to a program's query back to the PTY.
//
// The emulator, not the program, is the one that knows the answer to "what terminal are
// you" or "what is the cursor at", so the reply originates inside the emulator and has to
// travel outwards. It bypasses the input lock deliberately: this is the terminal
// answering, not a person typing, and REQ-TERM-008 is about people.
type ReplyFunc func(data []byte)

// EmulatorFactory builds an emulator for a given size.
//
// reply is how the emulator answers device queries. A session whose replies go nowhere
// looks broken to any program that asks the terminal a question: fish waits two seconds
// for a Primary Device Attributes answer and then permanently disables features, and a
// program querying the cursor position waits forever.
type EmulatorFactory func(size domain.Size, reply ReplyFunc) (Emulator, error)

// Scanner reads shell-integration markers out of one session's output stream
// (REQ-BLK-001, REQ-BLK-002, REQ-BLK-004).
//
// It is a port rather than a function in the service because the sequences it recognises
// are the other half of the bootstrap scripts: the two have to change together, and both
// live in the shellinteg adapter.
//
// The events it returns, and the bytes inside them, are only valid until the next call.
type Scanner interface {
	Scan(chunk []byte) []domain.Event
}

// ScannerFactory builds a scanner for one session. Each session needs its own, because a
// scanner carries the state of a sequence split across two PTY reads.
type ScannerFactory func() Scanner

// BlockStore persists blocks and their output (Data Model §2.2 and §2.3).
//
// Every method takes a context, and the one it is given is the session's own lifetime
// rather than any request's: a block outlives the call that started its command, and a
// chunk written half way through a build belongs to no request at all.
type BlockStore interface {
	// Create writes a new block's row (REQ-BLK-001).
	Create(ctx context.Context, block domain.Block) error
	// AppendChunk stores one run of raw output, compressed (Data Model §2.3). seq orders
	// the chunks within the block.
	AppendChunk(ctx context.Context, blockID string, seq int64, raw []byte) error
	// SetState records a state change on an open block (REQ-BLK-004).
	SetState(ctx context.Context, blockID string, state domain.BlockState) error
	// Finish closes the block, storing its exit code, its counters and the plain-text
	// transcript the agent and FTS5 read (REQ-BLK-002, REQ-BLK-007).
	Finish(ctx context.Context, block domain.Block, plain string) error
}

// Blocks is the module's second inbound port: reading the history back (API Spec §5.10
// through §5.12).
//
// It is separate from Sessions because the two have different lifetimes and different
// clients. A session is live state the daemon owns; a block is a row that outlives every
// session, and `umb` is allowed to read blocks without being allowed to drive terminals
// (API Spec §2).
type Blocks interface {
	// List pages through the history, newest first (API Spec §5.10).
	List(ctx context.Context, filter domain.BlockFilter) (domain.BlockPage, error)
	// Get returns one block, and optionally its output. The id may be domain.BlockLast,
	// which resolves to the most recent closed block (REQ-CLI-002).
	Get(ctx context.Context, id, sessionID string, include domain.Include) (domain.Block, domain.BlockOutput, error)
	// Search runs an FTS5 query over commands and transcripts (REQ-BLK-006).
	Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error)
}

// BlockReader is the outbound half: the queries the store knows how to answer.
//
// It is a separate interface from BlockStore so that the writing path and the reading path
// can be substituted apart, and because everything here is safe to run concurrently with a
// session that is still producing output.
type BlockReader interface {
	// List answers a filtered, paged query (API Spec §5.10).
	List(ctx context.Context, filter domain.BlockFilter) (domain.BlockPage, error)
	// Get returns one block by id.
	Get(ctx context.Context, id string) (domain.Block, error)
	// Last returns the most recent closed block, of one session when sessionID is set and
	// of the whole history otherwise (REQ-CLI-002).
	Last(ctx context.Context, sessionID string) (domain.Block, error)
	// Plain returns a block's transcript (REQ-BLK-007).
	Plain(ctx context.Context, id string) (string, error)
	// Raw reassembles a block's stored chunks, stopping at limit bytes and reporting
	// whether it stopped early.
	Raw(ctx context.Context, id string, limit int) (data []byte, truncated bool, err error)
	// Search runs the FTS5 query (REQ-BLK-006).
	Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error)
}
