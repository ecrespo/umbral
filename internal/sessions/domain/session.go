// Package domain holds the pure types and rules of the sessions module: what a session is,
// what sizes are legal and who is allowed to type into one.
//
// It imports nothing outside the standard library (Art. 3), which is what lets the policy
// here be tested without a PTY, a database or a terminal.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. The api layer maps them to JSON-RPC codes with errors.Is, so the rule
// and its wire representation stay in different packages (Tech Design §5.4).
var (
	// ErrNotFound reports an unknown session id.
	ErrNotFound = errors.New("sessions: not found")
	// ErrExited reports an operation on a session whose shell has already gone.
	ErrExited = errors.New("sessions: session has exited")
	// ErrInputLocked reports input refused because the lock belongs to someone else
	// (REQ-TERM-008).
	ErrInputLocked = errors.New("sessions: input is locked")
	// ErrValidation reports parameters the contract rejects.
	ErrValidation = errors.New("sessions: invalid parameters")
)

// State is a session's lifecycle state (Data Model §2.1).
type State string

const (
	// StateAlive means the PTY is open and the shell is running.
	StateAlive State = "alive"
	// StateExited means the shell has gone. The blocks stay (REQ-TERM-005).
	StateExited State = "exited"
)

// Integration reports whether shell integration was detected (REQ-BLK-003).
type Integration string

const (
	// IntegrationPending means no OSC sequence has arrived yet and the five-second
	// window has not closed.
	IntegrationPending Integration = "pending"
	// IntegrationOSC133 means the shell is emitting the sequences blocks derive from.
	IntegrationOSC133 Integration = "osc133"
	// IntegrationNone means the window closed with no sequences; output still flows,
	// but no blocks are created.
	IntegrationNone Integration = "none"
)

// InputOwner is who currently holds the write lock on a session's PTY (DD-003).
type InputOwner string

const (
	// InputOwnerHuman is the default: a person types into the session.
	InputOwnerHuman InputOwner = "human"
	// InputOwnerAgent means the agent is driving this PTY, and human input is refused
	// with INPUT_LOCKED (REQ-TERM-008).
	InputOwnerAgent InputOwner = "agent"
)

// Size limits from API Spec §5.3 and the Data Model CHECK constraints. They are here
// rather than only in SQL so a bad size is refused before it reaches a forkpty call.
const (
	MinCols = 20
	MaxCols = 1000
	MinRows = 5
	MaxRows = 500
)

// MaxScrollbackLines is how much history a snapshot may carry (REQ-TERM-004, API Spec
// §5.5). It lives here because it is a promise the product makes, not a property of
// whichever emulator happens to be behind the port.
const MaxScrollbackLines = 10_000

// MaxInputBytes is the per-message input limit from API Spec §5.7.
const MaxInputBytes = 64 << 10

// MaxEnvKeys is the environment limit from API Spec §5.3.
const MaxEnvKeys = 64

// Size is a terminal's dimensions in cells.
type Size struct {
	Cols uint16
	Rows uint16
}

// Validate reports whether the size is inside the contract's range.
func (s Size) Validate() error {
	if s.Cols < MinCols || s.Cols > MaxCols {
		return fmt.Errorf("%w: cols is %d, must be between %d and %d",
			ErrValidation, s.Cols, MinCols, MaxCols)
	}
	if s.Rows < MinRows || s.Rows > MaxRows {
		return fmt.Errorf("%w: rows is %d, must be between %d and %d",
			ErrValidation, s.Rows, MinRows, MaxRows)
	}
	return nil
}

// Session is one PTY session, as stored (Data Model §2.1) and as published (API Spec §4).
type Session struct {
	ID            string
	Shell         string
	CWD           string
	Size          Size
	State         State
	Integration   Integration
	InputOwner    InputOwner
	OwnerThreadID string
	ExitCode      *int
	CreatedAt     time.Time
	ExitedAt      *time.Time
}

// CanAcceptInputFrom reports whether a writer may type into this session, and why not.
//
// This is the whole of REQ-TERM-008: a human whose session has been taken over by the
// agent is refused before a single byte reaches the PTY, because a stray keystroke landing
// in the middle of an agent's command is exactly the confusion the lock exists to prevent.
func (s Session) CanAcceptInputFrom(writer InputOwner) error {
	if s.State == StateExited {
		return fmt.Errorf("%w: %s", ErrExited, s.ID)
	}
	if s.InputOwner != writer {
		return fmt.Errorf("%w: session %s belongs to %s, not %s",
			ErrInputLocked, s.ID, s.InputOwner, writer)
	}
	return nil
}

// CreateParams is a validated `session.create` request (API Spec §5.3).
type CreateParams struct {
	Shell            string
	CWD              string
	Env              map[string]string
	Size             Size
	ShellIntegration bool
}

// Validate checks the parameters the daemon can judge without touching the filesystem.
// Whether the shell is executable and the directory exists is an adapter's job, since
// those are questions about the world rather than about the contract.
func (p CreateParams) Validate() error {
	if err := p.Size.Validate(); err != nil {
		return err
	}
	if len(p.Env) > MaxEnvKeys {
		return fmt.Errorf("%w: env has %d keys, at most %d are allowed",
			ErrValidation, len(p.Env), MaxEnvKeys)
	}
	for key := range p.Env {
		if key == "" {
			return fmt.Errorf("%w: env contains an empty key", ErrValidation)
		}
	}
	return nil
}

// ValidateInput checks one `session.input` payload against the per-message limit.
func ValidateInput(data []byte) error {
	if len(data) > MaxInputBytes {
		return fmt.Errorf("%w: input is %d bytes, at most %d are allowed per message",
			ErrValidation, len(data), MaxInputBytes)
	}
	return nil
}
