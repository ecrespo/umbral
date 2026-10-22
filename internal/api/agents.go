package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// API Spec §5.39: agent.explain says which third-party agent detection found in a pane, which
// manifest and rule matched, and who decides the pane's state (REQ-XAG-003). It scans the pane
// afresh and decides nothing.

const capabilityAgentDetection = "agent_detection"

func hasAgents(cfg Config) bool { return cfg.Agents != nil }

// agentMethods is open to `umb`: `umb agent explain` is REQ-XAG-003's CLI.
func agentMethods() map[string]method {
	return map[string]method{
		"agent.explain": {
			handle: handleAgentExplain, kinds: treeClients, params: agentExplainParams{}, result: agentExplainResult{},
		},
	}
}

type agentExplainParams struct {
	PaneID string `json:"pane_id"`
}

type agentExplainResult struct {
	PaneID         string                 `json:"pane_id"`
	DetectedAgent  *DetectedAgent         `json:"detected_agent"`
	Manifest       *agentManifestOrigin   `json:"manifest"`
	Rule           *agentRule             `json:"rule"`
	StateAuthority agentStateAuthority    `json:"state_authority"`
	Skipped        []agentSkippedManifest `json:"skipped_manifests"`
}

type agentManifestOrigin struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Origin      string `json:"origin"`
}

// agentRule names the process by pid and executable basename, never by its arguments, which
// can carry a secret.
type agentRule struct {
	Kind       string `json:"kind"`
	Pattern    string `json:"pattern"`
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
}

type agentStateAuthority struct {
	AttentionState string  `json:"attention_state"`
	StateSource    *string `json:"state_source"`
}

type agentSkippedManifest struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

func handleAgentExplain(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc := c.server.cfg.Agents
	if svc == nil {
		return nil, fmt.Errorf("%w: agent.explain is not wired in", ErrNotImplemented)
	}
	var p agentExplainParams
	if err := decode(raw, &p, "agent.explain"); err != nil {
		return nil, err
	}
	if p.PaneID == "" {
		return nil, ValidationError("pane_id is required", ErrorField{Field: "pane_id", Issue: requiredTag})
	}
	ex, err := svc.Explain(ctx, p.PaneID)
	if err != nil {
		return nil, err
	}
	out := agentExplainResult{
		PaneID: ex.PaneID,
		StateAuthority: agentStateAuthority{
			AttentionState: ex.AttentionState, StateSource: emptyAsNull(ex.StateSource),
		},
		Skipped: make([]agentSkippedManifest, 0, len(ex.Skipped)),
	}
	if d := ex.Detection; d != nil {
		out.DetectedAgent = &DetectedAgent{Name: d.Manifest.Name, StateSource: d.Manifest.StateSource}
		out.Manifest = &agentManifestOrigin{Name: d.Manifest.Name, DisplayName: d.Manifest.DisplayName, Origin: d.Manifest.Origin}
		out.Rule = &agentRule{Kind: d.Rule, Pattern: d.Pattern, PID: d.PID, Executable: d.Executable}
	}
	for _, s := range ex.Skipped {
		out.Skipped = append(out.Skipped, agentSkippedManifest{File: s.File, Reason: s.Reason})
	}
	return out, nil
}
