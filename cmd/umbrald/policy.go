package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/agents/adapters/threadstore"
	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/security"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	secports "github.com/ecrespo/umbral/internal/security/ports"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
)

// isRepo is the write root's test for a repository root (Tech Design §5.3). The agent
// runtime and policy.explain share it, so an explanation and a turn agree about a write.
func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// newExplainer wires policy.explain (T-F1-28) to the thread store the runtime decides with
// and the registry its tools are declared in.
func newExplainer(db *store.Store, tools *registry.Registry) (*security.Explainer, error) {
	st, err := threadstore.New(db)
	if err != nil {
		return nil, err
	}
	return security.NewExplainer(security.ExplainConfig{
		Source: explainSource{threads: st}, Tools: toolCatalog{tools: tools}, IsRepo: isRepo,
	})
}

// threadReader is the part of the thread store policy.explain may touch: two reads.
type threadReader interface {
	Thread(ctx context.Context, id string) (agentsdomain.Thread, error)
	Rules(ctx context.Context, threadID string) ([]secdomain.Rule, error)
}

// explainSource reads a thread's mode, directory and rules for policy.explain, and nothing
// else: the security module may not import agents, so the root adapts one to the other.
type explainSource struct {
	threads threadReader
}

var _ secports.ExplainSource = explainSource{}

func (s explainSource) ThreadPolicy(ctx context.Context, threadID string) (secports.ThreadPolicy, error) {
	t, err := s.threads.Thread(ctx, threadID)
	if errors.Is(err, agentsdomain.ErrNotFound) {
		return secports.ThreadPolicy{}, fmt.Errorf("%w: %s", secdomain.ErrUnknownThread, threadID)
	}
	if err != nil {
		return secports.ThreadPolicy{}, err
	}
	return secports.ThreadPolicy{Mode: t.Mode, Cwd: t.Cwd}, nil
}

func (s explainSource) Rules(ctx context.Context, threadID string) ([]secdomain.Rule, error) {
	return s.threads.Rules(ctx, threadID)
}

// toolCatalog answers what a registered tool declares — the built-ins and every connected MCP
// server's tools — and shapes a path the way the built-ins do.
type toolCatalog struct {
	tools *registry.Registry
}

var _ secports.ToolCatalog = toolCatalog{}

func (c toolCatalog) Risk(tool string) (secdomain.Risk, bool) {
	for _, spec := range c.tools.Specs() {
		if spec.Name == tool {
			return spec.Risk, true
		}
	}
	return "", false
}

func (toolCatalog) PolicyPath(cwd, p string) string { return builtin.PolicyPath(cwd, p) }
