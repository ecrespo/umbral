package domain

import (
	"fmt"
	"strings"
)

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
	Summary string
	Diff    string
	State   ApprovalState
	// Accepted are the hunks of Diff the user approved, nil when it was approved whole. They
	// travel with the answer to the paused turn and are not stored: the call's result says
	// which were applied.
	Accepted  []string
	Scope     Scope
	CreatedAt int64
	DecidedAt *int64
}

// HunksSelectable reports whether single hunks of the approval's diff may be accepted: a diff of
// Umbral's own tool, never a hosted agent's write, which is answered without its content (delta
// `2026-10-hunks-file-tree`, decision 5).
func (a Approval) HunksSelectable() bool {
	return a.Diff != "" && !strings.HasPrefix(a.Tool, "acp:")
}

// Response is approval.respond's input (API §5.25).
type Response struct {
	ApprovalID string
	Approve    bool
	Scope      Scope
	// Hunks, when not nil, are the only hunks of the diff approved (decision 3).
	Hunks []string
}

// Validate checks the scope, and that hunks come with an approval for this call alone.
func (r Response) Validate() error {
	switch r.Scope {
	case ScopeOnce, ScopeThread, ScopeAlways:
	default:
		return fmt.Errorf("%w: scope %q is not once, thread or always", ErrValidation, r.Scope)
	}
	switch {
	case r.Hunks == nil:
		return nil
	case !r.Approve:
		return fmt.Errorf("%w: hunks are the ones approved; deny to accept none", ErrValidation)
	case r.Scope != ScopeOnce:
		return fmt.Errorf("%w: hunks are approved once: a rule approves whole calls", ErrValidation)
	case len(r.Hunks) == 0:
		return fmt.Errorf("%w: name at least one hunk, or deny", ErrValidation)
	}
	return nil
}
