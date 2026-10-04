// Package ports is what the TUI model renders from. The implementation lives in
// `internal/tui/adapters/`, and `cmd/umbral-tui` is the only place that picks one.
package ports

import (
	"context"
	"errors"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// Screen is one session's VT state as the client sees it.
//
// The client keeps its own, because DD-001 sends it bytes rather than cells: the daemon's
// emulator is authoritative for snapshots and for the block history, and this one exists
// only to turn the stream into something that can be drawn inside a pane. They are the
// same implementation on purpose — two emulators reading one byte stream are two chances
// to disagree about what the user is looking at.
type Screen interface {
	// Write feeds it the bytes of a snapshot or of a `session.output` notification. The
	// two are the same kind of thing: replayable VT (REQ-TERM-004).
	Write(p []byte) (int, error)
	// Resize changes the screen's dimensions. The caller is responsible for telling the
	// daemon too, through `session.resize`: this only changes what the client draws.
	Resize(size sessdomain.Size) error
	// Lines renders the visible screen, one string per row, top to bottom. Exactly
	// `size.Rows` entries, so a caller can place them without counting.
	Lines() ([]string, error)
	// Cursor reports the cursor's cell.
	Cursor() (x, y uint16, err error)
	// Reset empties the screen and its scrollback, leaving it as a newly created one of
	// the same size.
	//
	// It exists because a snapshot's replay contract is precise: feeding it to an *empty*
	// emulator of the same size reproduces the daemon's screen. Writing one into a screen
	// that already holds the session's history would layer the fresh rows over stale
	// ones, which is what re-attaching after a dropped subscription would otherwise do.
	Reset() error
	// Close releases it.
	Close() error
}

// ScreenFactory builds a screen of a given size. `cmd/umbral-tui` supplies it, so the
// model never names an implementation and the tests can hand it a fake.
type ScreenFactory func(size sessdomain.Size) (Screen, error)

// Daemon is what the TUI needs from `umbrald`. It is a narrow view of the JSON-RPC
// surface, not a mirror of it: the model calls these and nothing else, so a fake in a test
// is a few methods rather than a socket.
//
// Every method is the client's, so every one can fail because the daemon went away. The
// model treats that as a disconnection to recover from rather than as a fatal error.
type Daemon interface {
	// CreateSession starts a shell and returns it.
	CreateSession(ctx context.Context, size sessdomain.Size) (sessdomain.Session, error)
	// Subscribe attaches to a session and returns the screen as it stands, as replayable
	// VT, plus the sequence number that snapshot is current as of (REQ-TERM-004). Output
	// after it arrives as notifications.
	Subscribe(ctx context.Context, sessionID string) (snapshot []byte, seq uint64, err error)
	// Input sends keystrokes to a session.
	Input(ctx context.Context, sessionID string, data []byte) error
	// Resize tells the daemon a session's pane changed size (REQ-TERM-007).
	Resize(ctx context.Context, sessionID string, size sessdomain.Size) error
	// Blocks lists a session's blocks, newest first.
	Blocks(ctx context.Context, sessionID string, limit int) ([]sessdomain.Block, error)

	// CreateThread starts an agent thread working in cwd, in the default mode
	// (`thread.create`).
	CreateThread(ctx context.Context, cwd string) (threadID string, err error)
	// Send sends a message to a thread, with its attachments (`thread.send`). The adapter
	// gives every call a fresh client_msg_id (REQ-AGT-015); the panel never retries a send,
	// so there is no second call that would need the first one's.
	Send(ctx context.Context, threadID, text string, attachments []Attachment) (turnID string, err error)
	// Respond answers an approval: decision `approve` or `deny`, scope `once`, `thread` or
	// `always` (`approval.respond`).
	Respond(ctx context.Context, approvalID, decision, scope string) error
	// Cancel stops a thread's running turn (`thread.cancel`). stopped is false when there
	// was no turn to stop: the daemon answered `stopped_at: null`.
	Cancel(ctx context.Context, threadID string) (stopped bool, err error)
	// Approvals lists the approvals still pending, of every thread, oldest first
	// (`approval.list`).
	Approvals(ctx context.Context) ([]Approval, error)
	// Events is closed when the connection ends; the model then shows a disconnected
	// state instead of a frozen screen.
	Events() <-chan Event
}

// ErrApprovalGone is Respond's error for an approval that was already decided or has
// expired (`CONFLICT`, `NOT_FOUND`): there is nothing left to answer.
var ErrApprovalGone = errors.New("the approval was already decided or has expired")

// Attachment is one of thread.send's attachments: kind `file`, `dir` or `block`, and the
// path or block id it names (REQ-CTX-002).
type Attachment struct {
	Kind string
	Ref  string
}

// Approval is a tool call waiting for the user (API §4's Approval, the fields the panel
// shows).
type Approval struct {
	ID       string
	ThreadID string
	Tool     string
	Risk     string
	Reason   string
	Summary  string
	Diff     string
}

// EventKind says what an Event carries.
type EventKind int

// The events the TUI reacts to. Everything else the daemon publishes is ignored rather
// than an error: API Spec §9 makes new notifications an additive change, and a client that
// failed on one it did not know would break on every daemon upgrade.
const (
	// EventOutput is a chunk of a session's output (`session.output`).
	EventOutput EventKind = iota
	// EventBlockClosed is a command that finished (`block.closed`), which is when the
	// block list changes.
	EventBlockClosed
	// EventSessionExited is a shell that ended (`session.exited`).
	EventSessionExited
	// EventUnsubscribed is one subscription dropped for falling behind
	// (`session.unsubscribed`, API Spec §6 and §8). The connection is still up and the
	// recovery is to subscribe again, which brings a fresh snapshot; it is not a
	// disconnection.
	EventUnsubscribed
	// EventDisconnected is the stream ending. Err says why.
	EventDisconnected
	// EventThreadDelta is a chunk of a thread's answer (`thread.delta`); Reasoning marks
	// the model's thinking rather than its answer.
	EventThreadDelta
	// EventToolCall is a tool call's status change (`thread.tool_call`).
	EventToolCall
	// EventApprovalRequested is a turn paused on the user (`approval.requested`).
	EventApprovalRequested
	// EventTurnFinished is a turn's end (`thread.turn_finished`).
	EventTurnFinished
)

// Event is one thing that happened, flattened so the model does not decode JSON.
type Event struct {
	Kind      EventKind
	SessionID string
	// Data is the output bytes, for EventOutput.
	Data []byte
	// Seq is the sequence number of an output chunk: the session's own, from
	// `session.output`'s params (REQ-TERM-004, API Spec §5.11). It is not the envelope
	// counter of §6 — that one numbers notifications across the whole daemon, and a client
	// that anchored a screen to it would place bytes against the wrong ruler.
	Seq uint64
	// Err is why the connection ended, for EventDisconnected.
	Err error
	// CWD is where a closed block's command ran, for EventBlockClosed.
	CWD string

	// ThreadID names the thread of the agent events.
	ThreadID string
	// Text and Reasoning are a delta's, for EventThreadDelta.
	Text      string
	Reasoning bool
	// ToolCallID, Tool and Status are a tool call's, for EventToolCall.
	ToolCallID string
	Tool       string
	Status     string
	// Approval is what is asked, for EventApprovalRequested.
	Approval Approval
	// StopReason is how the turn ended, for EventTurnFinished.
	StopReason string
}
