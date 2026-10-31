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

	// CreateThread starts an agent thread working in cwd, in the default mode, belonging to
	// workspaceID, or to none when it is "" (`thread.create`, REQ-ORC-003).
	CreateThread(ctx context.Context, cwd, workspaceID string) (threadID string, err error)
	// Threads lists the threads that are not ephemeral, of every workspace, with the
	// workspace the daemon has focused (`session.snapshot`): the panel shows the focused
	// workspace's (REQ-ORC-003).
	Threads(ctx context.Context) (ThreadList, error)
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
	// Focus tells the daemon this client views threadID, or no thread when it is ""
	// (`thread.focus`, REQ-AGT-016).
	Focus(ctx context.Context, threadID string) error
	// Approvals lists the approvals still pending, of every thread, oldest first
	// (`approval.list`).
	Approvals(ctx context.Context) ([]Approval, error)
	// McpServers lists the configured MCP servers with their live state (`mcp.server.list`),
	// and Skills the installed skills (`skill.list`). A daemon without that module answers an
	// empty list, not an error: having no MCP client or skill store is a configuration.
	McpServers(ctx context.Context) ([]McpServer, error)
	Skills(ctx context.Context) ([]Skill, error)
	// TakeOver gives the user a thread's terminal and returns its session
	// (`thread.take_over`, REQ-FTU-002); HandBack gives it back to the agent
	// (`thread.hand_back`).
	TakeOver(ctx context.Context, threadID string) (sessionID string, err error)
	HandBack(ctx context.Context, threadID string) error
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

// ThreadList is the daemon's threads and its focused workspace, "" when none is focused.
// Seq is the snapshot's envelope `seq` (API §5.3): listings are asked for concurrently, and
// one older than the last applied is stale.
type ThreadList struct {
	Seq         uint64
	WorkspaceID string
	Threads     []Thread
}

// Thread is a thread as the panel lists it (API §4's Thread): WorkspaceID and WorktreeID are
// "" for none. Agent is the hosted ACP agent's name, "" for Umbral's own (REQ-ACP-001), and
// EgressVisibility `observed` or `unobserved` (REQ-ACP-007).
type Thread struct {
	ID          string
	Title       string
	WorkspaceID string
	WorktreeID  string
	// Attention is `attention_state`; State is the thread's `state`.
	Attention        string
	State            string
	Queued           bool
	Agent            string
	EgressVisibility string
}

// McpServer is an MCP server as the panel shows it (API §4's McpServer): LastError is ""
// when there is none.
type McpServer struct {
	Name      string
	State     string
	LastError string
}

// Skill is an installed skill as the panel shows it (API §4's Skill).
type Skill struct {
	Name    string
	Enabled bool
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
	// EventReconnecting is the daemon closing the stream while this client dials it again, as
	// on a live handoff (REQ-HND-001); Err says why it closed. EventReconnected is the new
	// connection: every subscription and the thread list are gone with the old one, and the
	// model attaches again from fresh snapshots.
	EventReconnecting
	EventReconnected
	// EventThreadDelta is a chunk of a thread's answer (`thread.delta`); Reasoning marks
	// the model's thinking rather than its answer.
	EventThreadDelta
	// EventToolCall is a tool call's status change (`thread.tool_call`).
	EventToolCall
	// EventApprovalRequested is a turn paused on the user (`approval.requested`).
	EventApprovalRequested
	// EventTurnFinished is a turn's end (`thread.turn_finished`).
	EventTurnFinished
	// EventNotification is a notification for the user (`notification.posted`, API Spec
	// §5.35): Title, Text as its body, and Sound.
	EventNotification
	// EventMcpServerState is an MCP server's state change (`mcp.server_state`), and
	// EventSkillChanged a skill installed, replaced, enabled, disabled or removed
	// (`skill.changed`). Both carry nothing: the panel lists again, which the daemon answers
	// from what it stored before it published (DD-007).
	EventMcpServerState
	EventSkillChanged
	// EventAttentionChanged is a thread's attention state changing
	// (`thread.attention_changed`), and EventWorkspaceFocused the daemon's focused workspace
	// changing (`workspace.focused`, or `workspace.closed`, which can move it). The panel
	// lists its threads again on either.
	EventAttentionChanged
	EventWorkspaceFocused
	// EventBlockSuggestion is a local model's one line for a failed block
	// (`block.suggestion`, REQ-AAI-001): BlockID and SessionID, and Text.
	EventBlockSuggestion
	// EventRemoteState is a remote host's link changing (`remote.state_changed`,
	// REQ-RMT-002): Host and Status, its state. Panes of a host that comes back are attached
	// again.
	EventRemoteState
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
	// Reason is why a subscription was dropped, for EventUnsubscribed.
	Reason string
	// Host is the remote host, for EventRemoteState.
	Host string
	// CWD is where a closed block's command ran, for EventBlockClosed.
	CWD string
	// BlockID is the closed block, for EventBlockClosed, and the block a suggestion is for, for
	// EventBlockSuggestion.
	BlockID string

	// ThreadID names the thread of the agent events.
	ThreadID string
	// Text and Reasoning are a delta's, for EventThreadDelta.
	Text      string
	Reasoning bool
	// ToolCallID, Tool and Status are a tool call's, for EventToolCall.
	ToolCallID string
	Tool       string
	Status     string
	// Skill is the skill a `skill_load` call names, from its args, for EventToolCall.
	Skill string
	// Approval is what is asked, for EventApprovalRequested.
	Approval Approval
	// StopReason is how the turn ended, for EventTurnFinished.
	StopReason string
	// Title and Sound are a notification's, for EventNotification; its body is in Text.
	Title string
	Sound string
}
