package ports

import (
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// Event kinds published by the sessions module. The names match the notification methods
// in API Spec §6, so an event can be followed from the bus to the wire without a
// translation table.
const (
	KindSessionOutput     bus.Kind = "session.output"
	KindSessionExited     bus.Kind = "session.exited"
	KindSessionResized    bus.Kind = "session.resized"
	KindSessionInputOwner bus.Kind = "session.input_owner"
)

// The events live in ports rather than in domain for one reason: they carry a bus.Kind,
// and domain may import nothing but the standard library (Art. 3). They are part of what
// the module publishes, which is what ports is for.

// SessionOutput carries a chunk of PTY output. T-F0-06 batches these before they reach a
// client; the bus itself only moves them.
type SessionOutput struct {
	SessionID string
	Seq       uint64
	Data      []byte
}

// EventKind implements bus.Event.
func (SessionOutput) EventKind() bus.Kind { return KindSessionOutput }

// SessionExited reports that the shell is gone (REQ-TERM-005). The session's blocks stay
// in the database; only the session's state changes.
type SessionExited struct {
	SessionID   string
	ExitCode    int
	ExitedAtMs  int64
	PreviousEnd bool
}

// EventKind implements bus.Event.
func (SessionExited) EventKind() bus.Kind { return KindSessionExited }

// SessionResized reports a new window size (REQ-TERM-007).
type SessionResized struct {
	SessionID string
	Size      domain.Size
}

// EventKind implements bus.Event.
func (SessionResized) EventKind() bus.Kind { return KindSessionResized }

// SessionInputOwner reports that the write lock changed hands (DD-003, REQ-TERM-008).
type SessionInputOwner struct {
	SessionID  string
	InputOwner domain.InputOwner
}

// EventKind implements bus.Event.
func (SessionInputOwner) EventKind() bus.Kind { return KindSessionInputOwner }
