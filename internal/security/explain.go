// Package security is the security module's root. It holds policy.explain (REQ-SEC-009, API
// Spec §5.36): it gathers what the agent runtime would decide with and asks the same engine,
// without running anything.
package security

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
)

// ExplainConfig is what the explainer needs.
type ExplainConfig struct {
	Source ports.ExplainSource
	Tools  ports.ToolCatalog
	// IsRepo says whether a directory is a repository root, for the write root; it must be
	// the agent runtime's own, or the two could disagree about a write.
	IsRepo func(dir string) bool
	// Policy is the engine over the destructive patterns in force — the agent runtime's own,
	// so the two cannot disagree (delta `2026-10-policy-explain`, decision 9); nil is the
	// built-in list.
	Policy func() domain.Policy
}

// Explainer implements ports.Explainer.
type Explainer struct {
	cfg ExplainConfig
}

var _ ports.Explainer = (*Explainer)(nil)

// NewExplainer builds the explainer.
func NewExplainer(cfg ExplainConfig) (*Explainer, error) {
	switch {
	case cfg.Source == nil:
		return nil, errors.New("security: an ExplainSource is required")
	case cfg.Tools == nil:
		return nil, errors.New("security: a ToolCatalog is required")
	case cfg.IsRepo == nil:
		return nil, errors.New("security: IsRepo is required")
	}
	if cfg.Policy == nil {
		cfg.Policy = domain.BuiltinRules().Policy
	}
	return &Explainer{cfg: cfg}, nil
}

// Explain evaluates a candidate action as a turn of its thread would: the thread's mode
// unless the request names one, the write root of the thread's directory, the thread's and
// the global rules, the risk the tool declares, a path target shaped as the built-in tools
// shape it, and domain.Decide.
func (e *Explainer) Explain(ctx context.Context, req domain.ExplainRequest) (domain.Decision, error) {
	if err := req.Validate(); err != nil {
		return domain.Decision{}, err
	}
	if declared, ok := e.cfg.Tools.Risk(req.Tool); ok && declared != req.Risk {
		return domain.Decision{}, fmt.Errorf("%w: %s declares %s, not %s", domain.ErrRiskMismatch, req.Tool, declared, req.Risk)
	}
	mode, cwd := req.Mode, ""
	if req.ThreadID != "" {
		t, err := e.cfg.Source.ThreadPolicy(ctx, req.ThreadID)
		if err != nil {
			return domain.Decision{}, err
		}
		if mode == "" {
			mode = t.Mode
		}
		cwd = t.Cwd
	}
	rules, err := e.cfg.Source.Rules(ctx, req.ThreadID)
	if err != nil {
		return domain.Decision{}, err
	}
	action := domain.Action{ThreadID: req.ThreadID, Tool: req.Tool, Risk: req.Risk, Target: req.Target}
	if cwd != "" {
		action.Cwd = e.cfg.Tools.PolicyPath("", cwd)
		action.WriteRoot = e.cfg.Tools.PolicyPath("", domain.WriteRoot(cwd, e.cfg.IsRepo))
	}
	// ReadOnly and WriteFS are the file tools' risks — every MCP tool is Network — and their
	// target is a path, which the rules match in the form the tool gives it.
	// Without a thread a relative path has no directory to be relative to, and resolving it
	// against the daemon's own would explain an action no turn forms: it stays as given.
	pathTool := req.Risk == domain.RiskReadOnly || req.Risk == domain.RiskWriteFS
	if pathTool && (cwd != "" || filepath.IsAbs(req.Target)) {
		action.Target = e.cfg.Tools.PolicyPath(cwd, req.Target)
	}
	return e.cfg.Policy().Decide(action, mode, rules, req.Tainted), nil
}
