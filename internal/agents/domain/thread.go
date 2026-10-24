// Package domain is the domain layer of the agents module, which owns the agent runtime:
// threads, turns, messages and tool calls (REQ-AGT-*). It imports nothing outside the
// standard library and other modules' domain (Art. 3).
package domain

import (
	"errors"
	"fmt"
	"strings"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// State is a thread's run state (API §4 Thread, `threads.state`).
type State string

// Thread states.
const (
	StateIdle             State = "idle"
	StateRunning          State = "running"
	StateAwaitingApproval State = "awaiting_approval"
	StateStopped          State = "stopped"
)

// StopReason is why a turn ended (API §6).
type StopReason string

// Stop reasons. StorageError and ContextOverflow are delta `2026-09-agent-runtime`'s.
const (
	StopEndTurn         StopReason = "end_turn"
	StopCancelled       StopReason = "cancelled"
	StopMaxSteps        StopReason = "max_steps"
	StopBudget          StopReason = "budget"
	StopToolError       StopReason = "tool_error"
	StopProviderError   StopReason = "provider_error"
	StopStorageError    StopReason = "storage_error"
	StopContextOverflow StopReason = "context_overflow"
	// StopAgentError is an ACP agent that exited, sent something that is not ACP, or fell
	// silent (REQ-ACP-006).
	StopAgentError StopReason = "agent_error"
)

// Egress visibility (`threads.egress_visibility`, REQ-ACP-007): whether Umbral sees the
// thread's network traffic. Umbral's own agent's is all its own model calls and `fetch_url`;
// a hosted ACP agent's process sends what it likes.
const (
	EgressObserved   = "observed"
	EgressUnobserved = "unobserved"
)

// Model classes (`threads.model_class`).
var modelClasses = map[string]bool{"fast": true, "code": true, "plan": true}

// Defaults and bounds of a thread (Data Model §2.4, API §5.19).
const (
	DefaultMaxSteps     = 50
	MaxMaxSteps         = 200
	DefaultBudgetTokens = 400000
	DefaultModelClass   = "code"
	MaxMessageChars     = 100000
)

// Thread is API §4's Thread.
type Thread struct {
	ID             string
	Title          string
	Mode           secdomain.Mode
	Model          string
	ModelClass     string
	Cwd            string
	State          State
	AttentionState string
	Ephemeral      bool
	MaxSteps       int
	BudgetTokens   int64
	TokensUsed     int64
	CostMicroUSD   int64
	CreatedAt      int64
	UpdatedAt      int64
	// Stalled is true while the thread's running turn is marked stalled (REQ-AUT-008). It is
	// the runtime's, never stored: it describes a turn of this daemon run.
	Stalled bool
	// Queued is true while the thread's turn is accepted but held for a slot under
	// `[agents] max_concurrent_turns` (REQ-ORC-002). The thread is `running` meanwhile.
	Queued bool
	// FullAuto is the thread's `full_auto` flag (REQ-ORC-004): its turns decide under
	// `full-auto` while its worktree is active, and under Mode otherwise. Mode is the mode the
	// thread returns to when the flag clears.
	FullAuto bool
	// WorkspaceID is the workspace the thread was created in, "" for none (REQ-ORC-003).
	WorkspaceID string
	// WorktreeID is the last worktree made for the thread, whatever its state now, "" for none.
	// Whether the thread runs in it is the worktree's state: only an active one is used.
	WorktreeID string
	// WorktreeRefusal is git's refusal of the worktree thread.create asked for (REQ-WT-008): the
	// thread was made without one and works in its own directory. Set by Create only, never
	// stored.
	WorktreeRefusal error
	// Agent is the `[[acp.agents]]` entry hosting the thread, "" for Umbral's own agent
	// (REQ-ACP-001); EgressVisibility is EgressUnobserved for such a thread (REQ-ACP-007).
	Agent            string
	EgressVisibility string
	// AgentUnavailable is why thread.create could not start the thread's agent once the
	// thread was written; the next thread.send tries again. Set by Create only, never stored.
	AgentUnavailable error
}

// CreateParams is thread.create's input (API §5.19).
type CreateParams struct {
	Mode         secdomain.Mode
	Model        string
	ModelClass   string
	Cwd          string
	Title        string
	Ephemeral    bool
	MaxSteps     int
	BudgetTokens int64
	// Workspace is the workspace the thread belongs to, "" for none (REQ-ORC-003).
	Workspace string
	// Worktree makes the thread's worktree with it (REQ-WT-001): a thread git refuses one to
	// is not created.
	Worktree bool
	// Agent hosts the thread in an `[[acp.agents]]` entry instead of Umbral's own agent
	// (REQ-ACP-001).
	Agent string
}

// Validate fills the defaults and checks the bounds.
func (p CreateParams) Validate() (CreateParams, error) {
	if p.Mode == "" {
		p.Mode = secdomain.ModeNormal
	}
	if p.Mode == secdomain.ModeFullAuto {
		// A new thread's worktree is not active until thread.create has made it; full-auto is
		// entered afterwards with thread.update (delta `2026-10-full-auto`).
		return p, fmt.Errorf("%w: a thread enters full-auto with thread.update once its worktree is active", ErrValidation)
	}
	if p.Agent != "" && (p.Model != "" || p.ModelClass != "") {
		return p, fmt.Errorf("%w: an ACP thread's model is its agent's: model and model_class are not set with agent", ErrValidation)
	}
	if !validMode(p.Mode) {
		return p, fmt.Errorf("%w: mode %q is not ask, normal or auto-edit", ErrValidation, p.Mode)
	}
	if p.ModelClass == "" {
		p.ModelClass = DefaultModelClass
	}
	if !modelClasses[p.ModelClass] {
		return p, fmt.Errorf("%w: model_class %q is not fast, code or plan", ErrValidation, p.ModelClass)
	}
	if p.Cwd == "" || !strings.HasPrefix(p.Cwd, "/") {
		return p, fmt.Errorf("%w: cwd must be an absolute path", ErrValidation)
	}
	if p.MaxSteps == 0 {
		p.MaxSteps = DefaultMaxSteps
	}
	if p.MaxSteps < 1 || p.MaxSteps > MaxMaxSteps {
		return p, fmt.Errorf("%w: max_steps must be 1-%d", ErrValidation, MaxMaxSteps)
	}
	if p.BudgetTokens == 0 {
		p.BudgetTokens = DefaultBudgetTokens
	}
	if p.BudgetTokens < 0 {
		return p, fmt.Errorf("%w: budget_tokens must be positive", ErrValidation)
	}
	return p, nil
}

func validMode(m secdomain.Mode) bool {
	return m == secdomain.ModeAsk || m == secdomain.ModeNormal || m == secdomain.ModeAutoEdit
}

// WireMode is the mode API §4 reports: `full-auto` while the flag is set, the thread's own mode
// otherwise.
func (t Thread) WireMode() secdomain.Mode {
	if t.FullAuto {
		return secdomain.ModeFullAuto
	}
	return t.Mode
}

// UpdateParams is thread.update's input (API §5.22); nil leaves a field as it is. A Mode of
// full-auto sets the thread's flag and leaves its own mode; any other Mode sets the mode and
// clears the flag.
type UpdateParams struct {
	Mode  *secdomain.Mode
	Model *string
	Title *string
}

// Validate checks the fields present.
func (p UpdateParams) Validate() error {
	if p.Mode != nil && *p.Mode != secdomain.ModeFullAuto && !validMode(*p.Mode) {
		return fmt.Errorf("%w: mode %q is not ask, normal, auto-edit or full-auto", ErrValidation, *p.Mode)
	}
	return nil
}

// Errors the module returns; internal/api maps them to the wire.
var (
	ErrNotFound   = errors.New("not found")
	ErrValidation = errors.New("invalid parameters")
	// ErrTurnRunning is a worktree change asked of a thread whose turn is running: its turn
	// keeps the directory and write root it started with (delta `2026-10-worktree-engine`,
	// decision 19). It is a conflict.
	ErrTurnRunning = fmt.Errorf("%w: a worktree change waits for the turn to end", ErrConflict)
	// ErrConflict is a thread.send while a turn runs, or a mode change during one.
	ErrConflict = errors.New("a turn is running")
	// ErrBudgetExceeded is a thread.send on a thread that has spent its token budget or its
	// cost cap.
	ErrBudgetExceeded = errors.New("the thread's budget is spent")
	// ErrProviderUnavailable is a thread.send whose thread has no model that can serve it:
	// none of its candidates is known and not down (API §5.20).
	ErrProviderUnavailable = errors.New("no model can serve the thread")
	// ErrThreadBlocked is a thread.send with a wait on a thread paused on an approval
	// (REQ-AUT-002): nothing is persisted and no wait starts.
	ErrThreadBlocked = errors.New("the thread is awaiting an approval")
)

// AttentionAfter is the attention state a turn's end leaves (API §7): `idle` after a cancel
// left the thread stopped or when a client views the thread as the turn ends, and `done` —
// finished, not yet viewed — after any other end (REQ-AGT-016).
func AttentionAfter(state State, viewed bool) string {
	if state == StateStopped || viewed {
		return "idle"
	}
	return "done"
}
