package domain

import "fmt"

// ApprovalState is an approval's state (API §4 Approval).
type ApprovalState string

// Approval states. An approval still pending when its turn is cancelled, or when the daemon
// restarts, is expired.
const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalDenied   ApprovalState = "denied"
	ApprovalExpired  ApprovalState = "expired"
)

// Scope is how far a decision reaches (`approvals.decision_scope`).
type Scope string

// Decision scopes: this call, this thread, or every thread.
const (
	ScopeOnce   Scope = "once"
	ScopeThread Scope = "thread"
	ScopeAlways Scope = "always"
)

// Approval is API §4's Approval: a tool call the policy answered `ask` for (REQ-AGT-004).
type Approval struct {
	ID         string
	ThreadID   string
	ToolCallID string
	Tool       string
	Risk       string
	// Reason is the policy's: policy, destructive, tainted or outside_write_root.
	Reason string
	// Summary is the call's target — the command line, the path, the URL — which a
	// remembered decision's rule matches.
	Summary   string
	Diff      string
	State     ApprovalState
	Scope     Scope
	CreatedAt int64
	DecidedAt *int64
}

// Response is approval.respond's input (API §5.25).
type Response struct {
	ApprovalID string
	Approve    bool
	Scope      Scope
}

// Validate checks the scope.
func (r Response) Validate() error {
	switch r.Scope {
	case ScopeOnce, ScopeThread, ScopeAlways:
		return nil
	default:
		return fmt.Errorf("%w: scope %q is not once, thread or always", ErrValidation, r.Scope)
	}
}
