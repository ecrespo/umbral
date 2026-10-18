package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	intdomain "github.com/ecrespo/umbral/internal/integrations/domain"
	intports "github.com/ecrespo/umbral/internal/integrations/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// The integration surface of API Spec §5.31: an external process reports a pane's semantic
// state, and gives the pane back (REQ-INT-002, REQ-INT-003, REQ-INT-005, REQ-INT-006). They
// are `pane.*` by name and `integrations` by capability: a daemon serving the tree without
// this module must not advertise them as part of it.

const capabilityIntegrations = "integrations"

func hasIntegrations(cfg Config) bool { return cfg.Integrations != nil }

func integrations(c *conn) (intports.Integrations, error) {
	if c.server.cfg.Integrations == nil {
		return nil, fmt.Errorf("%w: the integration surface is not wired in", ErrNotImplemented)
	}
	return c.server.cfg.Integrations, nil
}

// integrationMethods carries no kinds: API Spec §2 gives `pane.*` but `pane.move` to `umb`,
// which is how a hook reports (`umb pane report_state`), and to every interactive client.
func integrationMethods() map[string]method {
	return map[string]method{
		"pane.report_state":  {handle: handlePaneReportState, params: reportStateParams{}, result: emptyResult{}},
		"pane.release_state": {handle: handlePaneReleaseState, params: releaseStateParams{}, result: emptyResult{}},
		"pane.report_metadata": {
			handle: handlePaneReportMetadata, params: reportMetadataParams{}, result: emptyResult{},
		},
	}
}

// reportStateParams is §5.31's `report_state`. `seq` is a pointer because its absence means
// "not numbered", which is not the same as 0.
type reportStateParams struct {
	PaneID  string `json:"pane_id"`
	Source  string `json:"source"`
	Agent   string `json:"agent" api:"optional"`
	State   string `json:"state"`
	Message string `json:"message" api:"optional"`
	Seq     *int64 `json:"seq"`
}

type releaseStateParams struct {
	PaneID string `json:"pane_id"`
	Source string `json:"source"`
}

func handlePaneReportState(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := integrations(c)
	if err != nil {
		return nil, err
	}
	var p reportStateParams
	if err := decode(raw, &p, "pane.report_state"); err != nil {
		return nil, err
	}
	if err := svc.ReportState(ctx, intdomain.Report{
		PaneID: p.PaneID, Source: p.Source, Agent: p.Agent, State: intdomain.State(p.State),
		Message: p.Message, Seq: p.Seq,
	}); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

func handlePaneReleaseState(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := integrations(c)
	if err != nil {
		return nil, err
	}
	var p releaseStateParams
	if err := decode(raw, &p, "pane.release_state"); err != nil {
		return nil, err
	}
	if err := svc.ReleaseState(ctx, p.PaneID, p.Source); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

// reportMetadataParams is §5.32's `report_metadata`, as the schema publishes it. Every value
// may be `null`, which clears its key, so the handler reads which members were sent from the
// raw object rather than from these pointers, which cannot tell `null` from absent.
type reportMetadataParams struct {
	PaneID      string             `json:"pane_id"`
	Source      string             `json:"source"`
	Title       *string            `json:"title"`
	DisplayName *string            `json:"display_name"`
	StateLabels map[string]*string `json:"state_labels" api:"optional"`
	Tokens      map[string]*string `json:"tokens" api:"optional"`
	TTLMillis   *int64             `json:"ttl_ms"`
	Seq         *int64             `json:"seq"`
}

func handlePaneReportMetadata(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := integrations(c)
	if err != nil {
		return nil, err
	}
	var p reportMetadataParams
	if err := decode(raw, &p, "pane.report_metadata"); err != nil {
		return nil, err
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sent); err != nil {
		return nil, ValidationError("params must be an object")
	}
	_, titleSent := sent["title"]
	_, nameSent := sent["display_name"]
	null := func(member string) bool { v, ok := sent[member]; return ok && string(v) == "null" }
	if err := svc.ReportMetadata(ctx, intdomain.MetadataFields{
		PaneID: p.PaneID, Source: p.Source,
		Title: p.Title, TitleSent: titleSent, DisplayName: p.DisplayName, DisplayNameSent: nameSent,
		StateLabels: p.StateLabels, Tokens: p.Tokens, TTLMillis: p.TTLMillis, Seq: p.Seq,
		StateLabelsNull: null("state_labels"), TokensNull: null("tokens"),
	}); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

// toWireMetadata is API §4's `metadata`: the members a pane has, and `{}` when it has none.
func toWireMetadata(m wsdomain.Metadata) map[string]any {
	out := map[string]any{}
	if m.Title != "" {
		out["title"] = m.Title
	}
	if m.DisplayName != "" {
		out["display_name"] = m.DisplayName
	}
	if len(m.StateLabels) > 0 {
		out["state_labels"] = m.StateLabels
	}
	if len(m.Tokens) > 0 {
		out["tokens"] = m.Tokens
	}
	return out
}

// integrationsDomainError maps the module's sentinels; a pane it cannot find is the tree's
// NOT_FOUND, which workspaceDomainError already translates.
func integrationsDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, intdomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, intdomain.ErrPermissionDenied):
		return codePermissionDenied, domainPermissionDenied, true
	default:
		return 0, "", false
	}
}

// paneStateChangedPayload is §6's `pane.state_changed`.
type paneStateChangedPayload struct {
	PaneID         string  `json:"pane_id"`
	AttentionState string  `json:"attention_state"`
	StateSource    *string `json:"state_source"`
}
