package api

import (
	"context"
	"encoding/json"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
)

// Approval is API Spec §4's Approval.
type Approval struct {
	ID            string  `json:"id"`
	ThreadID      string  `json:"thread_id"`
	ToolCallID    string  `json:"tool_call_id"`
	Tool          string  `json:"tool"`
	Risk          string  `json:"risk"`
	Reason        string  `json:"reason"`
	Summary       string  `json:"summary"`
	Diff          *string `json:"diff"`
	State         string  `json:"state"`
	DecisionScope *string `json:"decision_scope"`
	CreatedAt     int64   `json:"created_at"`
	DecidedAt     *int64  `json:"decided_at"`
}

func toWireApproval(a agentsdomain.Approval) Approval {
	return Approval{
		ID: a.ID, ThreadID: a.ThreadID, ToolCallID: a.ToolCallID, Tool: a.Tool, Risk: a.Risk, Reason: a.Reason,
		Summary: a.Summary, Diff: emptyAsNull(a.Diff), State: string(a.State), DecisionScope: emptyAsNull(string(a.Scope)),
		CreatedAt: a.CreatedAt, DecidedAt: a.DecidedAt,
	}
}

// approvalMethods is §5.24–§5.25. §2's cli row names no approval.*: answering one is an
// interactive client's act.
func approvalMethods() map[string]method {
	return map[string]method{
		"approval.list":    {handle: handleApprovalList, kinds: interactiveClients, params: listApprovalsParams{}, result: approvalListResult{}},
		"approval.respond": {handle: handleApprovalRespond, kinds: interactiveClients, params: respondParams{}, result: Approval{}},
	}
}

type listApprovalsParams struct {
	ThreadID string `json:"thread_id" api:"optional"`
	All      bool   `json:"all" api:"optional"`
}

type approvalListResult struct {
	Items []Approval `json:"items"`
}

func handleApprovalList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p listApprovalsParams
	if err := decode(raw, &p, "approval.list"); err != nil {
		return nil, err
	}
	list, err := svc.Approvals(ctx, p.ThreadID, p.All)
	if err != nil {
		return nil, err
	}
	out := approvalListResult{Items: make([]Approval, 0, len(list))}
	for _, a := range list {
		out.Items = append(out.Items, toWireApproval(a))
	}
	return out, nil
}

type respondParams struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	Scope      string `json:"scope"`
}

func handleApprovalRespond(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p respondParams
	if err := decode(raw, &p, "approval.respond"); err != nil {
		return nil, err
	}
	if p.ApprovalID == "" {
		return nil, ValidationError("approval_id is required", ErrorField{Field: "approval_id", Issue: requiredTag})
	}
	if p.Decision != "approve" && p.Decision != "deny" {
		return nil, ValidationError("decision must be approve or deny", ErrorField{Field: "decision", Issue: "invalid"})
	}
	a, err := svc.Respond(ctx, agentsdomain.Response{
		ApprovalID: p.ApprovalID, Approve: p.Decision == "approve", Scope: agentsdomain.Scope(p.Scope),
	})
	if err != nil {
		return nil, err
	}
	return toWireApproval(a), nil
}
