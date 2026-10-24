// Package ports is the ports layer of the agents module: what the runtime persists through,
// the model gateway it calls, the approval flow it waits on, and the events it publishes.
package ports

import (
	"context"
	"errors"
	"iter"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/bus"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// ErrDuplicate is a message whose client id the thread already has (REQ-AGT-015).
var ErrDuplicate = errors.New("the thread already has this client_msg_id")

// Store persists threads, their messages and their tool calls. Every write the runtime
// publishes an event for comes first (DD-007).
type Store interface {
	CreateThread(ctx context.Context, t domain.Thread) error
	// DeleteThread removes a thread nothing refers to yet: one whose worktree was refused as it
	// was being created.
	DeleteThread(ctx context.Context, id string) error
	// SetWorktree records the thread's latest worktree.
	SetWorktree(ctx context.Context, threadID, worktreeID string, now int64) error
	// ThreadsInWorkspace lists a workspace's threads, ephemeral ones included, oldest first.
	ThreadsInWorkspace(ctx context.Context, workspaceID string) ([]domain.Thread, error)
	Thread(ctx context.Context, id string) (domain.Thread, error)
	// Threads lists the threads that are not ephemeral, most recently updated first.
	Threads(ctx context.Context) ([]domain.Thread, error)
	UpdateThread(ctx context.Context, id string, p domain.UpdateParams, now int64) (domain.Thread, error)

	// BeginTurn persists the user's message and marks the thread running, in one
	// transaction. A message whose client id the thread already has returns the original and
	// ErrDuplicate; a thread with a turn running is domain.ErrConflict; a thread past its
	// token budget, or whose cost has reached maxCostMicroUSD when it is above 0, is
	// domain.ErrBudgetExceeded; an unknown thread is domain.ErrNotFound. It returns
	// the thread as the transaction read it, which is what the turn runs with: a mode change
	// that lands after the send's own read cannot leave the turn with a stale mode.
	// queued writes the turn as held for a slot (REQ-ORC-002).
	BeginTurn(ctx context.Context, msg domain.Message, queued bool, maxCostMicroUSD, now int64) (domain.Message, domain.Thread, error)
	// Dequeue records that the thread's held turn took its slot and starts now.
	Dequeue(ctx context.Context, threadID string, now int64) error
	// AppendMessage persists a message of a running turn.
	AppendMessage(ctx context.Context, msg domain.Message) error
	// AppendContent adds streamed text to a message already persisted.
	AppendContent(ctx context.Context, messageID, text string) error
	// SaveToolCall inserts a tool call or updates its status and result.
	SaveToolCall(ctx context.Context, call domain.ToolCall) error
	// FinishTurn adds the turn's usage to the thread and sets its state, with the attention
	// state domain.AttentionAfter gives, and clears queued: a held turn cancelled ends here
	// too. A thread a client views is seen at now as well.
	FinishTurn(ctx context.Context, threadID string, state domain.State, viewed bool, usage domain.Usage, now int64) error
	// MarkSeen records that a client focused the thread at now, and moves its attention from
	// `done` to `idle` (REQ-AGT-016). changed reports that move; an unknown thread is
	// domain.ErrNotFound.
	MarkSeen(ctx context.Context, threadID string, now int64) (changed bool, err error)
	// TurnStatus reads the thread's state, attention state and latest turn in one query.
	TurnStatus(ctx context.Context, threadID string) (Status, error)

	// MessageByClientID finds the message a client id names in a thread.
	MessageByClientID(ctx context.Context, threadID, clientMsgID string) (domain.Message, bool, error)
	// Messages is a thread's history, oldest first.
	Messages(ctx context.Context, threadID string) ([]domain.Message, error)
	// ToolCalls is a thread's tool calls, oldest first.
	ToolCalls(ctx context.Context, threadID string) ([]domain.ToolCall, error)
	// Rules are the persisted policy rules that apply to a thread: its own and the global
	// ones (`policy_rules`).
	Rules(ctx context.Context, threadID string) ([]secdomain.Rule, error)

	// RequestApproval persists a pending approval and marks its thread awaiting_approval, in
	// one transaction.
	RequestApproval(ctx context.Context, a domain.Approval) error
	// Approval reads one approval.
	Approval(ctx context.Context, id string) (domain.Approval, error)
	// Approvals lists approvals oldest first: the pending ones, or all with all set; of one
	// thread when threadID is set.
	Approvals(ctx context.Context, threadID string, all bool) ([]domain.Approval, error)
	// DecideApproval records a decision on a pending approval — and the rule it remembers, when
	// rule is set — and marks its thread running again, in one transaction. An approval that is
	// not pending is domain.ErrConflict.
	DecideApproval(ctx context.Context, id string, state domain.ApprovalState, scope domain.Scope, rule *secdomain.Rule, now int64) (domain.Approval, error)
}

// Worktree is a thread's worktree as the runtime uses it: where its turns run (REQ-WT-002).
type Worktree struct {
	ID       string
	Path     string
	RepoRoot string // the main working copy's top level, a real path
}

// ErrWorktreeRefused wraps a refusal of Worktrees.Create the user can act on — the directory
// is not in a repository, the repository is bare, git is too old (REQ-WT-008) — as opposed to a
// failure of git or of the store.
var ErrWorktreeRefused = errors.New("agents: the worktree was refused")

// Worktrees is the worktree engine as the runtime reaches it; cmd/umbrald wires it.
type Worktrees interface {
	// Create makes the thread's worktree from the repository cwd is in, at base (HEAD when
	// empty). A refusal is the engine's error, passed through as it is.
	Create(ctx context.Context, threadID, cwd, base string) (Worktree, error)
	// Active is the thread's active worktree; a worktree gone from disk is not active
	// (REQ-WT-006).
	Active(ctx context.Context, threadID string) (Worktree, bool, error)
}

// WorktreeThreads is the runtime's side of worktree.*, which internal/api serves.
type WorktreeThreads interface {
	// CreateWorktree makes the thread's worktree and records it, while no turn runs.
	CreateWorktree(ctx context.Context, threadID, base string) (string, error)
	// WhileIdle runs fn while the thread has no turn running, holding off a new one until fn
	// returns: a merge or a discard never pulls a worktree from under a turn. A thread with a
	// turn running is domain.ErrTurnRunning; an unknown one domain.ErrNotFound.
	WhileIdle(ctx context.Context, threadID string, fn func() error) error
	// InWorkspace lists a workspace's threads, oldest first.
	InWorkspace(ctx context.Context, workspaceID string) ([]domain.Thread, error)
}

// Publisher is where the runtime announces what it persisted: the daemon's bus.
type Publisher interface {
	Publish(ev bus.Event)
}

// ModelCall is one call through the model gateway. Model, when set, is the only candidate;
// otherwise Class picks them.
type ModelCall struct {
	ThreadID string
	TurnID   string
	Class    string
	Model    string
	Request  llm.Request
}

// Models is the model gateway as the runtime sees it.
type Models interface {
	Stream(ctx context.Context, call ModelCall) (iter.Seq2[llm.Event, error], error)
	// Window is the smallest known context window among the call's candidates, 0 when none
	// is known (delta `2026-09-context-budget`, decision 3).
	Window(ctx context.Context, class, model string) int64
	// Available reports whether any candidate could serve a call of this class, or naming
	// this model: known to the catalog and not down.
	Available(ctx context.Context, class, model string) bool
	// Local reports whether any candidate of this class, or this model, is served on this
	// machine: such a turn gets the longer stall window (REQ-AUT-008).
	Local(ctx context.Context, class, model string) bool
}

// Threads is the module's inbound port, which internal/api serves as thread.*.
type Threads interface {
	Create(ctx context.Context, p domain.CreateParams) (domain.Thread, error)
	Send(ctx context.Context, p SendParams) (SendResult, error)
	Get(ctx context.Context, id string) (domain.Thread, error)
	List(ctx context.Context) ([]domain.Thread, error)
	Update(ctx context.Context, id string, p domain.UpdateParams) (domain.Thread, error)
	// Cancel is thread.cancel (API §5.21): it stops the running turn and returns when it has
	// ended, with the moment it did; nil when no turn was running.
	Cancel(ctx context.Context, threadID string) (*int64, error)
	Messages(ctx context.Context, threadID string) ([]domain.Message, error)
	// Approvals is approval.list (API §5.24).
	Approvals(ctx context.Context, threadID string, all bool) ([]domain.Approval, error)
	// Respond is approval.respond (API §5.25): it records the decision, and the rule a
	// `thread` or `always` scope remembers, then resumes the paused turn.
	Respond(ctx context.Context, r domain.Response) (domain.Approval, error)
	// Focus is thread.focus (API §5.23): viewer — a connection — now views threadID, or no
	// thread when it is "". Viewing a `done` thread makes it `idle`, and a turn that ends while
	// any viewer views its thread ends `idle` (REQ-AGT-016).
	Focus(ctx context.Context, viewer, threadID string) error
	// Unfocus forgets a viewer, as when its connection closes.
	Unfocus(viewer string)
	// Focused is the thread focused most recently by a viewer that still views one; "" when
	// no viewer views a thread.
	Focused() string
}

// MaxStdinBytes is REQ-CLI-001's limit on what `umb ai` sends from stdin: the decoded size
// of a `stdin` attachment's `data_b64`. It bounds the frame, not the prompt: the attachment
// is then capped at 256 KiB like any other (REQ-CTX-005).
const MaxStdinBytes = 1 << 20

// AttachmentRef is an attachment as thread.send names it: a `ref` for a file, a directory or
// a block, or, for `stdin`, the data itself (API §5.20).
type AttachmentRef struct {
	Kind string
	Ref  string
	// Data is a `stdin` attachment's content, never nil for one: what `umb ai` read.
	Data []byte
	// Truncated says the client stopped reading stdin at MaxStdinBytes, so more followed
	// that nobody read.
	Truncated bool
}

// SendParams is thread.send's input (API §5.20).
type SendParams struct {
	ThreadID    string
	Text        string
	Attachments []AttachmentRef
	ClientMsgID string
	// RejectBlocked is set by a send that brings a wait: on a thread awaiting an approval
	// it is ErrThreadBlocked rather than CONFLICT, and it is checked before a repeated
	// client_msg_id is looked up (REQ-AUT-002).
	RejectBlocked bool
}

// Status is one consistent reading of a thread for a wait: its state, its attention state
// and its latest turn, "" when it never had one (DD-011).
type Status struct {
	State     domain.State
	Attention string
	TurnID    string
}

// SendResult is thread.send's answer.
type SendResult struct {
	TurnID    string
	MessageID string
}

// Tracer traces the runtime's turns (REQ-OBS-001, Art. 7). Each method returns the context
// the span lives in — a turn's model calls, tools and log lines run inside it, which is how
// the router's `llm.call` spans and the log lines' trace ids join the turn — and the
// function that ends it.
type Tracer interface {
	// Turn starts a turn's span, the root of the turn's trace.
	Turn(ctx context.Context, threadID, turnID string) (context.Context, func(TurnTrace))
	// Tool starts a tool call's span inside the turn's.
	Tool(ctx context.Context, callID, tool string) (context.Context, func(ToolTrace))
}

// TurnTrace is how a turn ended, for its span.
type TurnTrace struct {
	StopReason domain.StopReason
	// Model is the catalog id that served the turn's last model call, if any did.
	Model string
	Usage domain.Usage
}

// ToolTrace is how a tool call ended, for its span. Risk is empty for a call that never got
// as far as being classified.
type ToolTrace struct {
	Status domain.ToolStatus
	Risk   string
}

// Metrics is what the runtime counts (Tech Design §7.2); internal/obs implements it.
type Metrics interface {
	// ToolCallInvalid counts one tool call whose tool or arguments did not validate, by the
	// model that made it: umbral_tool_calls_invalid_total (REQ-OBS-002).
	ToolCallInvalid(model string)
	// WaitStalled counts a turn marked stalled: umbral_waits_stalled_total (REQ-AUT-008,
	// REQ-OBS-004).
	WaitStalled()
}

// ErrAgentFailed is a hosted agent that failed its turn: it exited, sent something that is not
// ACP, or fell silent (REQ-ACP-006). The turn ends `agent_error`.
var ErrAgentFailed = errors.New("agents: the hosted agent failed")

// ErrAgentRefused is a hosted agent that may not run where it was asked to: no such entry, no
// manifest, a repository's own configuration of it (REQ-ACP-009). errors.As finds the
// *AgentRefusal.
var ErrAgentRefused = errors.New("agents: the hosted agent was refused")

// AgentRefusal is why a hosted agent may not run, with the reason API §5.19 gives in
// `data.reason`.
type AgentRefusal struct {
	Reason string
	Detail string
}

func (r *AgentRefusal) Error() string { return r.Detail }

// Is makes errors.Is(err, ErrAgentRefused) true for every refusal.
func (r *AgentRefusal) Is(target error) bool { return target == ErrAgentRefused }

// ExternalAgents hosts the threads created with an `agent` (REQ-ACP-001): third-party agents
// that speak ACP. cmd/umbrald wires internal/acp in.
type ExternalAgents interface {
	// Check refuses an agent that may not run in dir, before anything is written.
	Check(ctx context.Context, agent, dir string) error
	// Open launches the thread's agent in dir and opens its session.
	Open(ctx context.Context, req ExternalRequest) error
	// Prompt runs one turn of the thread's agent, launching it again if it is not running, and
	// returns how the turn ended. A turn the agent failed is ErrAgentFailed.
	Prompt(ctx context.Context, req ExternalRequest, text string, sink ExternalSink) (domain.StopReason, error)
	// Close stops the thread's agent.
	Close(threadID string)
}

// ExternalRequest names a thread's agent and the directory it runs in.
type ExternalRequest struct {
	ThreadID string
	Agent    string
	Dir      string
}

// ExternalToolCall is a tool call a hosted agent reports, as its updates left it so far.
type ExternalToolCall struct {
	// ID is the agent's own id for the call, unique within its session.
	ID       string
	Title    string
	Kind     string
	Risk     string
	Status   string // pending, in_progress, completed, failed
	RawInput []byte
	Output   string
	// Locations are the paths the call says it touches.
	Locations []string
	// Command, Cwd and URL are what the call's raw input names, for deciding its permission:
	// an `execute` call's command line and directory, a `fetch` call's URL.
	Command string
	Cwd     string
	URL     string
}

// The requests a hosted agent makes of Umbral that a turn decides and records (REQ-ACP-004,
// REQ-ACP-005).
const (
	OpReadFile  = "fs/read_text_file"
	OpWriteFile = "fs/write_text_file" //nolint:gosec // G101: an ACP method name, not a credential
	OpTerminal  = "terminal/create"
)

// ExternalOp is one of a hosted agent's requests of Umbral: a file read or write by its absolute
// Path (a write with its Content, for the approval's diff), or a terminal's Command line in Cwd.
type ExternalOp struct {
	Method  string
	Path    string
	Content string
	Command string
	// Policy is a terminal's line with one token per word the shell sees, decided with Command.
	Policy string
	// Script marks a terminal whose words hold shell syntax the policy cannot read; it asks.
	Script bool
	Cwd    string
}

// ErrDenied is a hosted agent's request the policy or the user did not allow.
var ErrDenied = errors.New("agents: the request was not allowed")

// ExternalSink is where a hosted agent's turn reports: the runtime persists each report
// before it publishes it (DD-007). An error is a write that failed and ends the turn.
type ExternalSink interface {
	Text(text string) error
	Thought(text string)
	ToolCall(call ExternalToolCall) error
	// Permission decides the agent's `session/request_permission` for call, through the
	// policy engine and, for an ask, the approval queue (REQ-ACP-003). It allows once or not
	// at all.
	Permission(ctx context.Context, call ExternalToolCall) (bool, error)
	// Request decides op and records it as a tool call of its own; allowed, it runs perform
	// with the target the decision was made on — a file's path with its links resolved, or
	// the command line — and records how that went (REQ-ACP-004, REQ-ACP-005). A request not
	// allowed is ErrDenied and perform is not run.
	Request(ctx context.Context, op ExternalOp, perform func(ctx context.Context, target string) (string, error)) error
}
