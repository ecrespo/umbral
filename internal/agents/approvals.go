package agents

import (
	"context"
	"errors"
	"strings"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

const prefixApproval = "apr"

// awaitApproval pauses the turn on an `ask` (REQ-AGT-004): the approval is persisted and the
// thread marked awaiting_approval before approval.requested is published (DD-007), and the
// turn waits for approval.respond. A turn cancelled meanwhile leaves the approval expired.
func (t *turnRun) awaitApproval(ctx context.Context, c domain.ToolCall, action secdomain.Action, decision secdomain.Decision, diff string) (bool, error) {
	r := t.r
	a := domain.Approval{
		ID: r.cfg.NewID(prefixApproval), ThreadID: t.thread.ID, ToolCallID: c.ID, Tool: c.Tool, Risk: c.Risk,
		Reason: decision.Reason, Summary: action.Target, Diff: diff, State: domain.ApprovalPending, CreatedAt: r.now(),
	}
	answer := make(chan domain.Approval, 1)
	r.mu.Lock()
	r.waiting[a.ID] = answer
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiting, a.ID)
		r.mu.Unlock()
	}()

	if err := r.cfg.Store.RequestApproval(ctx, a); err != nil {
		return false, storageErr(err)
	}
	r.publish(ports.ApprovalRequested{Approval: a})

	select {
	case decided := <-answer:
		if ctx.Err() != nil {
			// Answered and cancelled at once: the cancel wins, and the tool does not run.
			return false, ctx.Err()
		}
		if decided.Scope != domain.ScopeOnce {
			// A remembered decision, allow or deny, applies to the rest of this turn too.
			rules, err := r.cfg.Store.Rules(ctx, t.thread.ID)
			if err != nil {
				return false, storageErr(err)
			}
			t.rules = rules
		}
		return decided.State == domain.ApprovalApproved, nil
	case <-ctx.Done():
		_, err := r.cfg.Store.DecideApproval(context.WithoutCancel(ctx), a.ID, domain.ApprovalExpired, "", nil, r.now())
		if err != nil && !errors.Is(err, domain.ErrConflict) {
			r.cfg.Logger.WarnContext(ctx, "a cancelled turn's approval could not be expired", "approval", a.ID, "error", err)
		}
		return false, ctx.Err()
	}
}

// Approvals is approval.list: the pending approvals, or all of them.
func (r *Runtime) Approvals(ctx context.Context, threadID string, all bool) ([]domain.Approval, error) {
	return r.cfg.Store.Approvals(ctx, threadID, all)
}

// Respond is approval.respond (API §5.25). The decision — and, for `thread` or `always`, the
// rule that remembers it — is persisted before the paused turn resumes (REQ-AGT-011). A
// destructive command, or a target a rule's glob would read as a wider pattern, is decided for
// this call only: neither is remembered (REQ-SEC-005, delta `2026-09-approvals`).
func (r *Runtime) Respond(ctx context.Context, resp domain.Response) (domain.Approval, error) {
	if err := resp.Validate(); err != nil {
		return domain.Approval{}, err
	}
	a, err := r.cfg.Store.Approval(ctx, resp.ApprovalID)
	if err != nil {
		return domain.Approval{}, err
	}
	scope := resp.Scope
	if scope != domain.ScopeOnce && !rememberable(a) {
		scope = domain.ScopeOnce
	}
	state, verdict := domain.ApprovalDenied, secdomain.VerdictDeny
	if resp.Approve {
		state, verdict = domain.ApprovalApproved, secdomain.VerdictAllow
	}
	var rule *secdomain.Rule
	switch scope {
	case domain.ScopeThread:
		rule = &secdomain.Rule{ThreadID: a.ThreadID, Tool: a.Tool, Pattern: a.Summary, Decision: verdict, Source: secdomain.RuleSourceUser}
	case domain.ScopeAlways:
		rule = &secdomain.Rule{Tool: a.Tool, Pattern: a.Summary, Decision: verdict, Source: secdomain.RuleSourceUser}
	case domain.ScopeOnce:
	}
	decided, err := r.cfg.Store.DecideApproval(ctx, a.ID, state, scope, rule, r.now())
	if err != nil {
		return domain.Approval{}, err
	}
	r.mu.Lock()
	answer, waiting := r.waiting[a.ID]
	r.mu.Unlock()
	if waiting {
		answer <- decided
	}
	return decided, nil
}

// rememberable reports whether a decision on this approval may become a rule. A destructive
// command asks every time; a target holding `*` or `?` would, as a rule's glob, match more
// than the call the user saw; and a compound command line would never match again, since the
// rules read it one command at a time (delta `2026-09-policy-precedence`).
func rememberable(a domain.Approval) bool {
	if a.Reason == secdomain.ReasonDestructive || strings.ContainsAny(a.Summary, "*?") {
		return false
	}
	return a.Risk != string(secdomain.RiskExec) || len(secdomain.CommandParts(a.Summary)) == 1
}
