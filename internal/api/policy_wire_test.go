package api

import (
	"context"
	"fmt"
	"slices"
	"testing"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// fakeExplainer evaluates with the engine and no thread store: what the test sees is api's
// half, the method, the error mapping and the wire form.
type fakeExplainer struct{}

func (fakeExplainer) Explain(_ context.Context, req secdomain.ExplainRequest) (secdomain.Decision, error) {
	if err := req.Validate(); err != nil {
		return secdomain.Decision{}, err
	}
	if req.ThreadID != "" {
		return secdomain.Decision{}, fmt.Errorf("%w: %s", secdomain.ErrUnknownThread, req.ThreadID)
	}
	return secdomain.Decide(secdomain.Action{Tool: req.Tool, Risk: req.Risk, Target: req.Target}, req.Mode, nil, req.Tainted), nil
}

// TestPolicyExplainOverTheWire_REQ_SEC_009: `umb` may call policy.explain, which answers
// API §5.36's decision, deciding rule, reason and the ordered trace, every step evaluated
// and one marked decided.
func TestPolicyExplainOverTheWire_REQ_SEC_009(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{PolicyExplain: fakeExplainer{}})
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("hello: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), "policy_explain") {
		t.Errorf("policy_explain is not advertised: %v", capabilitiesOf(t, hello.Result))
	}

	resp := c.call(2, "policy.explain", map[string]any{
		"mode": "auto-edit", "tool": "run_command", "risk": "Exec", "target": "rm -rf build",
	})
	if resp.Error != nil {
		t.Fatalf("policy.explain: %+v", resp.Error)
	}
	want := `{"decision":"ask","deciding_rule":"destructive_pattern","reason":"destructive","allow_always":false,"trace":[` +
		`{"step":"exposure","matched":false,"decided":false},` +
		`{"step":"destructive_pattern","matched":true,"decided":true,"detail":"rm_recursive"},` +
		`{"step":"deny_rules","matched":false,"decided":false},` +
		`{"step":"taint","matched":false,"decided":false},` +
		`{"step":"mode","matched":false,"decided":false,"detail":"auto-edit"},` +
		`{"step":"allow_rules","matched":false,"decided":false},` +
		`{"step":"mode_default","matched":true,"decided":false,"detail":"auto-edit"}]}`
	if got := string(ordered[policyExplainResult](t, resp.Result)); got != want {
		t.Errorf("result =\n%s\nwant\n%s", got, want)
	}

	for name, tc := range map[string]struct {
		params map[string]any
		code   string
	}{
		"no mode and no thread": {map[string]any{"tool": "read_file", "risk": "ReadOnly", "target": "a"}, domainValidationError},
		"unknown risk":          {map[string]any{"mode": "normal", "tool": "read_file", "risk": "Root", "target": "a"}, domainValidationError},
		"unknown thread":        {map[string]any{"thread_id": "thr_nope", "tool": "read_file", "risk": "ReadOnly", "target": "a"}, domainNotFound},
	} {
		resp := c.call(3, "policy.explain", tc.params)
		if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != tc.code {
			t.Errorf("%s: %+v, want %s", name, resp.Error, tc.code)
		}
	}
}

// TestPolicyExplainIsNotImplementedWithoutTheModule: a daemon built without it answers
// NOT_IMPLEMENTED and does not advertise `policy_explain`.
func TestPolicyExplainIsNotImplementedWithoutTheModule(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	if hello := c.hello(s.Token(), ClientCLI); slices.Contains(capabilitiesOf(t, hello.Result), "policy_explain") {
		t.Error("policy_explain advertised without the module")
	}
	resp := c.call(2, "policy.explain", map[string]any{"mode": "normal", "tool": "t", "risk": "ReadOnly", "target": ""})
	if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("without the module: %+v", resp.Error)
	}
}
