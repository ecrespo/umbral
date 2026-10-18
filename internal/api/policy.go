package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// API Spec §5.36: policy.explain evaluates a candidate action with the agent runtime's own
// engine and answers the decision and its trace. It runs nothing and records nothing.

const capabilityPolicyExplain = "policy_explain"

func hasPolicyExplain(cfg Config) bool { return cfg.PolicyExplain != nil }

// policyMethods is open to `umb`: `umb policy explain` is REQ-SEC-009's CLI.
func policyMethods() map[string]method {
	return map[string]method{
		"policy.explain": {
			handle: handlePolicyExplain, kinds: treeClients, params: policyExplainParams{}, result: policyExplainResult{},
		},
	}
}

type policyExplainParams struct {
	ThreadID string `json:"thread_id" api:"optional"`
	Mode     string `json:"mode" api:"optional"`
	Tool     string `json:"tool"`
	Risk     string `json:"risk"`
	Target   string `json:"target"`
	Tainted  bool   `json:"tainted" api:"optional"`
}

type policyExplainResult struct {
	Decision     string             `json:"decision"`
	DecidingRule string             `json:"deciding_rule"`
	Reason       string             `json:"reason,omitempty"`
	AllowAlways  bool               `json:"allow_always"`
	Trace        []policyTraceEntry `json:"trace"`
}

type policyTraceEntry struct {
	Step    string `json:"step"`
	Matched bool   `json:"matched"`
	Decided bool   `json:"decided"`
	Detail  string `json:"detail,omitempty"`
	Note    string `json:"note,omitempty"`
}

func handlePolicyExplain(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc := c.server.cfg.PolicyExplain
	if svc == nil {
		return nil, fmt.Errorf("%w: policy.explain is not wired in", ErrNotImplemented)
	}
	var p policyExplainParams
	if err := decode(raw, &p, "policy.explain"); err != nil {
		return nil, err
	}
	d, err := svc.Explain(ctx, secdomain.ExplainRequest{
		ThreadID: p.ThreadID, Mode: secdomain.Mode(p.Mode), Tool: p.Tool,
		Risk: secdomain.Risk(p.Risk), Target: p.Target, Tainted: p.Tainted,
	})
	if err != nil {
		return nil, err
	}
	out := policyExplainResult{
		Decision: string(d.Verdict), DecidingRule: string(d.Step), Reason: d.Reason,
		AllowAlways: d.AllowAlways, Trace: make([]policyTraceEntry, 0, len(d.Trace)),
	}
	for _, s := range d.Trace {
		out.Trace = append(out.Trace, policyTraceEntry{
			Step: string(s.Step), Matched: s.Matched, Decided: s.Decided, Detail: s.Detail, Note: s.Note,
		})
	}
	return out, nil
}

// policyDomainError maps policy.explain's two sentinels onto §3.
func policyDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, secdomain.ErrInvalidExplain):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, secdomain.ErrUnknownThread):
		return codeNotFound, domainNotFound, true
	}
	return 0, "", false
}
