package api

import (
	"context"
	"fmt"
	"slices"
	"testing"

	intdomain "github.com/ecrespo/umbral/internal/integrations/domain"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// fakeDetector explains w1:p1 as running Codex, and knows no other pane.
type fakeDetector struct{}

func (fakeDetector) Explain(_ context.Context, paneID string) (intdomain.Explanation, error) {
	if paneID != "w1:p1" {
		return intdomain.Explanation{}, fmt.Errorf("%w: %s", wsdomain.ErrNotFound, paneID)
	}
	m := intdomain.Builtins()[1]
	return intdomain.Explanation{
		PaneID: "w1:p1",
		Detection: &intdomain.ExplainedDetection{
			Manifest: m, Rule: intdomain.RuleExecutable, Pattern: "codex", PID: 4242, Executable: "codex",
		},
		AttentionState: "working", StateSource: "codex-hooks",
		Skipped: []intdomain.SkippedManifest{{File: "/cfg/agents/bad.toml", Reason: "invalid agent manifest: no rule"}},
	}, nil
}

// TestAgentExplainOverTheWire_REQ_XAG_003: `umb` may call agent.explain, advertised as
// `agent_detection`, which answers API §5.39's object; an unknown pane is NOT_FOUND and a
// missing pane_id VALIDATION_ERROR.
func TestAgentExplainOverTheWire_REQ_XAG_003(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{Agents: fakeDetector{}})
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("hello: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), "agent_detection") {
		t.Errorf("agent_detection is not advertised: %v", capabilitiesOf(t, hello.Result))
	}
	resp := c.call(2, "agent.explain", map[string]any{"pane_id": "w1:p1"})
	if resp.Error != nil {
		t.Fatalf("agent.explain: %+v", resp.Error)
	}
	want := `{"pane_id":"w1:p1","detected_agent":{"name":"codex","state_source":"hooks"},` +
		`"manifest":{"name":"codex","display_name":"Codex","origin":"builtin"},` +
		`"rule":{"kind":"executable","pattern":"codex","pid":4242,"executable":"codex"},` +
		`"state_authority":{"attention_state":"working","state_source":"codex-hooks"},` +
		`"skipped_manifests":[{"file":"/cfg/agents/bad.toml","reason":"invalid agent manifest: no rule"}]}`
	if got := string(ordered[agentExplainResult](t, resp.Result)); got != want {
		t.Errorf("result =\n%s\nwant\n%s", got, want)
	}
	for name, tc := range map[string]struct {
		params map[string]any
		code   string
	}{
		"unknown pane": {map[string]any{"pane_id": "w9:p9"}, domainNotFound},
		"no pane":      {map[string]any{}, domainValidationError},
	} {
		resp := c.call(3, "agent.explain", tc.params)
		if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != tc.code {
			t.Errorf("%s: %+v, want %s", name, resp.Error, tc.code)
		}
	}
}

// TestAgentExplainIsNotImplementedWithoutTheModule: a daemon built without detection answers
// NOT_IMPLEMENTED and does not advertise `agent_detection`.
func TestAgentExplainIsNotImplementedWithoutTheModule(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	if hello := c.hello(s.Token(), ClientCLI); slices.Contains(capabilitiesOf(t, hello.Result), "agent_detection") {
		t.Error("agent_detection advertised without the module")
	}
	resp := c.call(2, "agent.explain", map[string]any{"pane_id": "w1:p1"})
	if resp.Error == nil || resp.Error.Message == "" || resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("agent.explain: %+v, want NOT_IMPLEMENTED", resp.Error)
	}
}
