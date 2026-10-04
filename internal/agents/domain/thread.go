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
}

// Validate fills the defaults and checks the bounds.
func (p CreateParams) Validate() (CreateParams, error) {
	if p.Mode == "" {
		p.Mode = secdomain.ModeNormal
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

// UpdateParams is thread.update's input (API §5.22); nil leaves a field as it is.
type UpdateParams struct {
	Mode  *secdomain.Mode
	Model *string
	Title *string
}

// Validate checks the fields present.
func (p UpdateParams) Validate() error {
	if p.Mode != nil && !validMode(*p.Mode) {
		return fmt.Errorf("%w: mode %q is not ask, normal or auto-edit", ErrValidation, *p.Mode)
	}
	return nil
}

// Errors the module returns; internal/api maps them to the wire.
var (
	ErrNotFound   = errors.New("not found")
	ErrValidation = errors.New("invalid parameters")
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
// left the thread stopped, `done` — finished, not yet viewed — after any other end.
func AttentionAfter(state State) string {
	if state == StateStopped {
		return "idle"
	}
	return "done"
}
