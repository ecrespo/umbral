package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	intdomain "github.com/ecrespo/umbral/internal/integrations/domain"
	intports "github.com/ecrespo/umbral/internal/integrations/ports"
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
