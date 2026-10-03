package ports

import (
	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/bus"
)

// Event kinds published by the agents module; the names are API §6's notification methods.
const (
	KindThreadDelta        bus.Kind = "thread.delta"
	KindThreadToolCall     bus.Kind = "thread.tool_call"
	KindThreadTurnFinished bus.Kind = "thread.turn_finished"
	KindContextCompacted   bus.Kind = "context.compacted"
	KindApprovalRequested  bus.Kind = "approval.requested"
)

// ApprovalRequested is a turn paused on a decision (REQ-AGT-004).
type ApprovalRequested struct {
	Approval domain.Approval
}

// EventKind implements bus.Event.
func (ApprovalRequested) EventKind() bus.Kind { return KindApprovalRequested }

// ThreadDelta is a chunk of the model's answer (REQ-AGT-001). Kind is "text" or "reasoning".
type ThreadDelta struct {
	ThreadID string
	TurnID   string
	Kind     string
	Text     string
}

// EventKind implements bus.Event.
func (ThreadDelta) EventKind() bus.Kind { return KindThreadDelta }

// ThreadToolCall is a tool call, published on every status change.
type ThreadToolCall struct {
	Call domain.ToolCall
}

// EventKind implements bus.Event.
func (ThreadToolCall) EventKind() bus.Kind { return KindThreadToolCall }

// TurnFinished is a turn's end (REQ-AGT-008, REQ-LLM-005).
type TurnFinished struct {
	ThreadID   string
	TurnID     string
	StopReason domain.StopReason
	Usage      domain.Usage
	// EndState is what the end left for a wait to observe: `stopped` after a cancel, else
	// the attention state written with it. A wait pinned to this turn reads it here,
	// because the store may already show the next turn (DD-011). It is not on the wire.
	EndState string
}

// EventKind implements bus.Event.
func (TurnFinished) EventKind() bus.Kind { return KindThreadTurnFinished }

// ContextCompacted reports a compaction (REQ-CTX-004).
type ContextCompacted struct {
	ThreadID     string
	BeforeTokens int64
	AfterTokens  int64
}

// EventKind implements bus.Event.
func (ContextCompacted) EventKind() bus.Kind { return KindContextCompacted }
