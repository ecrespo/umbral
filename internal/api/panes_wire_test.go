package api

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	intdomain "github.com/ecrespo/umbral/internal/integrations/domain"
	intports "github.com/ecrespo/umbral/internal/integrations/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

type fakeIntegrations struct {
	reports  []intdomain.Report
	released [][2]string
	metadata []intdomain.MetadataReport
}

func (f *fakeIntegrations) ReportMetadata(_ context.Context, fields intdomain.MetadataFields) error {
	r, err := fields.Build()
	if err != nil {
		return err
	}
	f.metadata = append(f.metadata, r)
	return nil
}

func (f *fakeIntegrations) ReportState(_ context.Context, r intdomain.Report) error {
	if err := r.Validate(); err != nil {
		return err
	}
	switch r.PaneID {
	case "w1:p9":
		return wsdomain.ErrNotFound
	case "w1:p7":
		return intdomain.ErrPermissionDenied
	}
	f.reports = append(f.reports, r)
	return nil
}

func (f *fakeIntegrations) ReleaseState(_ context.Context, paneID, source string) error {
	f.released = append(f.released, [2]string{paneID, source})
	return nil
}

// TestPaneReportsReachTheWire_REQ_INT_002: pane.report_state and pane.release_state answer
// `{}`, carry every §5.31 field to the module — `seq` absent as absent, not 0 — map its
// errors onto §3, are open to `umb`, and `pane.state_changed` reaches clients.
func TestPaneReportsReachTheWire_REQ_INT_002(t *testing.T) {
	t.Parallel()
	svc := &fakeIntegrations{}
	b := bus.New()
	s := testServerWithConfig(t, Config{Integrations: svc, Bus: b})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Notify(ctx)
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("handshake: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), capabilityIntegrations) {
		t.Errorf("integrations is not advertised: %v", capabilitiesOf(t, hello.Result))
	}

	resp := c.call(2, "pane.report_state", map[string]any{
		"pane_id": "w1:p1", "source": "claude-code", "agent": "claude", "state": "blocked",
		"message": "approve?", "seq": 4,
	})
	if resp.Error != nil || string(ordered[emptyResult](t, resp.Result)) != `{}` {
		t.Fatalf("report_state = %+v %+v", resp.Result, resp.Error)
	}
	if resp := c.call(3, "pane.report_state", map[string]any{
		"pane_id": "w1:p1", "source": "claude-code", "state": "idle",
	}); resp.Error != nil {
		t.Fatalf("report_state without seq = %+v", resp.Error)
	}
	if len(svc.reports) != 2 || svc.reports[0].Seq == nil || *svc.reports[0].Seq != 4 ||
		svc.reports[0].Agent != "claude" || svc.reports[0].Message != "approve?" || svc.reports[1].Seq != nil {
		t.Fatalf("the module got %+v", svc.reports)
	}

	for _, tc := range []struct {
		params map[string]any
		code   int
	}{
		{map[string]any{"pane_id": "w1:p1", "source": "a b", "state": "idle"}, codeValidationError},
		{map[string]any{"pane_id": "w1:p1", "source": "x", "state": "unknown"}, codeValidationError},
		{map[string]any{"pane_id": "w1:p9", "source": "x", "state": "idle"}, codeNotFound},
		{map[string]any{"pane_id": "w1:p7", "source": "x", "state": "idle"}, codePermissionDenied},
	} {
		if resp := c.call(4, "pane.report_state", tc.params); resp.Error == nil || resp.Error.Code != tc.code {
			t.Errorf("report_state %v = %+v, want %d", tc.params, resp.Error, tc.code)
		}
	}

	if resp := c.call(5, "pane.release_state", map[string]any{"pane_id": "w1:p1", "source": "claude-code"}); resp.Error != nil {
		t.Fatalf("release_state = %+v", resp.Error)
	}
	if len(svc.released) != 1 || svc.released[0] != [2]string{"w1:p1", "claude-code"} {
		t.Fatalf("the module released %+v", svc.released)
	}

	b.Publish(intports.PaneStateChanged{PaneID: "w1:p1", AttentionState: wsdomain.AttentionWorking, StateSource: "umbral:shell"})
	method, _, n := c.readNotification(t)
	if method != "pane.state_changed" || string(n) != `{"pane_id":"w1:p1","attention_state":"working","state_source":"umbral:shell"}` {
		t.Fatalf("pane.state_changed = %s %s", method, n)
	}
	b.Publish(intports.PaneStateChanged{PaneID: "w1:p1", AttentionState: wsdomain.AttentionUnknown})
	if _, _, n := c.readNotification(t); string(n) != `{"pane_id":"w1:p1","attention_state":"unknown","state_source":null}` {
		t.Fatalf("pane.state_changed with no source = %s", n)
	}
}

// TestPaneReportsNeedTheirModule_REQ_API_003: without the integration surface the two methods
// answer NOT_IMPLEMENTED and `integrations` is not advertised.
func TestPaneReportsNeedTheirModule_REQ_API_003(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{Bus: bus.New()})
	c := dial(t, s)
	resp := c.hello(s.Token(), ClientTUI)
	if resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if slices.Contains(capabilitiesOf(t, resp.Result), capabilityIntegrations) {
		t.Errorf("integrations advertised with no module behind it")
	}
	if resp := c.call(2, "pane.report_state", map[string]any{"pane_id": "w1:p1", "source": "x", "state": "idle"}); resp.Error == nil || resp.Error.Code != codeNotImplemented {
		t.Fatalf("report_state with no module = %+v", resp.Error)
	}
}

func capabilitiesOf(t *testing.T, result any) []string {
	t.Helper()
	var hello helloResult
	if err := json.Unmarshal(ordered[helloResult](t, result), &hello); err != nil {
		t.Fatal(err)
	}
	return hello.Capabilities
}

// TestPaneMetadataReachesTheWire_REQ_INT_004: pane.report_metadata answers `{}`, tells a
// `null` (clear) from an absent member (keep), maps validation onto §3, is open to `umb`, and
// a pane's metadata is API §4's `metadata` object.
func TestPaneMetadataReachesTheWire_REQ_INT_004(t *testing.T) {
	t.Parallel()
	svc := &fakeIntegrations{}
	s := testServerWithConfig(t, Config{Integrations: svc, Bus: bus.New()})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	resp := c.call(2, "pane.report_metadata", map[string]any{
		"pane_id": "w1:p1", "source": "claude-code", "title": "go test", "display_name": nil,
		"state_labels": map[string]any{"blocked": "needs you"}, "tokens": map[string]any{"summary": "unit", "old": nil},
		"ttl_ms": 5000, "seq": 3,
	})
	if resp.Error != nil || string(ordered[emptyResult](t, resp.Result)) != `{}` {
		t.Fatalf("report_metadata = %+v %+v", resp.Result, resp.Error)
	}
	if len(svc.metadata) != 1 {
		t.Fatalf("the module got %+v", svc.metadata)
	}
	r := svc.metadata[0]
	if r.Set["title"] != "go test" || r.Set["label:blocked"] != "needs you" || r.Set["token:summary"] != "unit" ||
		r.TTLMillis != 5000 || r.Seq == nil || *r.Seq != 3 || !slices.Contains(r.Clear, "display_name") || !slices.Contains(r.Clear, "token:old") {
		t.Errorf("the module got %+v", r)
	}
	if resp := c.call(3, "pane.report_metadata", map[string]any{"pane_id": "w1:p1", "source": "x", "title": "t"}); resp.Error != nil {
		t.Fatalf("a title alone = %+v", resp.Error)
	}
	if got := svc.metadata[1]; slices.Contains(got.Clear, "display_name") {
		t.Errorf("an absent display_name was cleared: %+v", got)
	}
	if resp := c.call(5, "pane.report_metadata", map[string]any{
		"pane_id": "w1:p1", "source": "x", "tokens": nil, "state_labels": nil,
	}); resp.Error != nil {
		t.Fatalf("null objects = %+v", resp.Error)
	}
	if got := svc.metadata[2]; !slices.Contains(got.ClearPrefixes, "token:") || !slices.Contains(got.ClearPrefixes, "label:") {
		t.Errorf("null tokens and state_labels did not clear: %+v", got)
	}
	if got := svc.metadata[1]; len(got.ClearPrefixes) != 0 {
		t.Errorf("absent objects cleared: %+v", got)
	}
	for _, params := range []map[string]any{
		{"pane_id": "w1:p1", "source": "x"},
		{"pane_id": "w1:p1", "source": "x", "title": "t", "ttl_ms": 0},
		{"pane_id": "w1:p1", "source": "x", "state_labels": map[string]any{"thinking": "x"}},
	} {
		if resp := c.call(4, "pane.report_metadata", params); resp.Error == nil || resp.Error.Code != codeValidationError {
			t.Errorf("report_metadata %v = %+v, want VALIDATION_ERROR", params, resp.Error)
		}
	}

	wire := toWireMetadata(wsdomain.Metadata{Title: "go test", Tokens: map[string]string{"summary": "unit"}})
	if raw, _ := json.Marshal(wire); string(raw) != `{"title":"go test","tokens":{"summary":"unit"}}` {
		t.Errorf("metadata on the wire = %s", raw)
	}
	if raw, _ := json.Marshal(toWireMetadata(wsdomain.Metadata{})); string(raw) != `{}` {
		t.Errorf("no metadata on the wire = %s", raw)
	}
}
