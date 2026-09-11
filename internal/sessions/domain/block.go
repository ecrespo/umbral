package domain

import (
	"fmt"
	"time"
)

// BlockState is a block's lifecycle state (Data Model §2.2, API Spec §4 and §7).
type BlockState string

const (
	// BlockRunning is a command that has started and not yet reported an exit code.
	BlockRunning BlockState = "running"
	// BlockInteractive is a running block that took over the alternate screen
	// (REQ-BLK-004). Its output is no longer recorded: what a full-screen program paints
	// is a picture, not a transcript.
	BlockInteractive BlockState = "interactive"
	// BlockFinished is a command that reported its exit code (REQ-BLK-002).
	BlockFinished BlockState = "finished"
	// BlockAbandoned is a block whose session died while it was still open.
	BlockAbandoned BlockState = "abandoned"
)

// BlockOrigin says who ran the command (Data Model §2.2).
type BlockOrigin string

const (
	// OriginUser is a command a person typed.
	OriginUser BlockOrigin = "user"
	// OriginAgent is a command the agent ran through run_command (F1).
	OriginAgent BlockOrigin = "agent"
)

// Output limits from the Data Model.
const (
	// MaxOutputPlainBytes caps the searchable plain text kept in the block row
	// (Data Model §2.2, REQ-BLK-007).
	MaxOutputPlainBytes = 1 << 20
	// MaxOutputRawBytes caps the compressed chunk history per block (Data Model §2.3).
	// Past it, storage stops and the block is marked truncated; the live stream to
	// clients is not interrupted, because a client watching a build must keep seeing it.
	MaxOutputRawBytes = 16 << 20
)

// IntegrationWindow is how long a session may stay `pending` before it is judged to have
// no shell integration (REQ-BLK-003).
//
// The window starts when the PTY starts, not when the first byte arrives: a shell that
// never prints anything is exactly the case this has to catch.
const IntegrationWindow = 5 * time.Second

// Block is one command and its output, as stored (Data Model §2.2) and as published
// (API Spec §4).
type Block struct {
	ID        string
	SessionID string
	Origin    BlockOrigin
	ThreadID  string
	Command   string
	CWD       string
	Host      string
	State     BlockState

	ExitCode  *int
	StartedAt time.Time
	EndedAt   *time.Time

	// OutputBytes counts the raw bytes the command produced, including the ones past
	// MaxOutputRawBytes that were never stored. It is a fact about the command, not
	// about the database, so a truncated block still reports how much it really wrote.
	OutputBytes int64
	// OutputTruncated is set once storage stopped at MaxOutputRawBytes.
	OutputTruncated bool
}

// DurationMs reports how long the command ran, or nil while it is still open.
func (b Block) DurationMs() *int64 {
	if b.EndedAt == nil {
		return nil
	}
	ms := b.EndedAt.Sub(b.StartedAt).Milliseconds()
	return &ms
}

// Open reports whether the block is still accumulating output.
func (b Block) Open() bool {
	return b.State == BlockRunning || b.State == BlockInteractive
}

// Validate checks the invariants the schema's CHECK constraints also enforce, so a bad
// block is refused before it reaches SQLite rather than as a constraint error afterwards.
func (b Block) Validate() error {
	switch b.State {
	case BlockRunning, BlockInteractive, BlockFinished, BlockAbandoned:
	default:
		return fmt.Errorf("%w: unknown block state %q", ErrValidation, b.State)
	}
	switch b.Origin {
	case OriginUser:
	case OriginAgent:
		if b.ThreadID == "" {
			return fmt.Errorf("%w: an agent block needs a thread", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: unknown block origin %q", ErrValidation, b.Origin)
	}
	if b.SessionID == "" {
		return fmt.Errorf("%w: a block needs a session", ErrValidation)
	}
	return nil
}
