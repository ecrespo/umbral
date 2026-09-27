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
	Thread(ctx context.Context, id string) (domain.Thread, error)
	// Threads lists the threads that are not ephemeral, most recently updated first.
	Threads(ctx context.Context) ([]domain.Thread, error)
	UpdateThread(ctx context.Context, id string, p domain.UpdateParams, now int64) (domain.Thread, error)

	// BeginTurn persists the user's message and marks the thread running, in one
	// transaction. A message whose client id the thread already has returns the original and
	// ErrDuplicate; a thread with a turn running is domain.ErrConflict; a thread past its
	// budget is domain.ErrBudgetExceeded; an unknown thread is domain.ErrNotFound. It returns
	// the thread as the transaction read it, which is what the turn runs with: a mode change
	// that lands after the send's own read cannot leave the turn with a stale mode.
	BeginTurn(ctx context.Context, msg domain.Message, now int64) (domain.Message, domain.Thread, error)
	// AppendMessage persists a message of a running turn.
	AppendMessage(ctx context.Context, msg domain.Message) error
	// AppendContent adds streamed text to a message already persisted.
	AppendContent(ctx context.Context, messageID, text string) error
	// SaveToolCall inserts a tool call or updates its status and result.
	SaveToolCall(ctx context.Context, call domain.ToolCall) error
	// FinishTurn adds the turn's usage to the thread and sets its state.
	FinishTurn(ctx context.Context, threadID string, state domain.State, usage domain.Usage, now int64) error

	// MessageByClientID finds the message a client id names in a thread.
	MessageByClientID(ctx context.Context, threadID, clientMsgID string) (domain.Message, bool, error)
	// Messages is a thread's history, oldest first.
	Messages(ctx context.Context, threadID string) ([]domain.Message, error)
	// ToolCalls is a thread's tool calls, oldest first.
	ToolCalls(ctx context.Context, threadID string) ([]domain.ToolCall, error)
	// Rules are the persisted policy rules that apply to a thread: its own and the global
	// ones (`policy_rules`).
	Rules(ctx context.Context, threadID string) ([]secdomain.Rule, error)
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
}

// ApprovalRequest is a tool call the policy answered `ask` for (REQ-AGT-004).
type ApprovalRequest struct {
	Call     domain.ToolCall
	Decision secdomain.Decision
	Summary  string
	Diff     string
}

// Approver pauses a turn on an `ask` until the user answers (T-F1-14). It returns whether the
// call was approved.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (bool, error)
}

// Threads is the module's inbound port, which internal/api serves as thread.*.
type Threads interface {
	Create(ctx context.Context, p domain.CreateParams) (domain.Thread, error)
	Send(ctx context.Context, p SendParams) (SendResult, error)
	Get(ctx context.Context, id string) (domain.Thread, error)
	List(ctx context.Context) ([]domain.Thread, error)
	Update(ctx context.Context, id string, p domain.UpdateParams) (domain.Thread, error)
	Messages(ctx context.Context, threadID string) ([]domain.Message, error)
}

// AttachmentRef is an attachment as thread.send names it.
type AttachmentRef struct {
	Kind string
	Ref  string
}

// SendParams is thread.send's input (API §5.20).
type SendParams struct {
	ThreadID    string
	Text        string
	Attachments []AttachmentRef
	ClientMsgID string
}

// SendResult is thread.send's answer.
type SendResult struct {
	TurnID    string
	MessageID string
}
