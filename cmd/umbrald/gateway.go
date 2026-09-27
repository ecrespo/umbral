package main

import (
	"context"
	"log/slog"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/llmgw"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/openaicompat"
	"github.com/ecrespo/umbral/internal/llmgw/adapters/openrouter"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// gateway is the model catalog as the daemon runs it: rebuilt from models.toml and the
// resolved credentials whenever they change, refreshed in the background, and served as
// model.list (API Spec §5.26).
type gateway struct {
	ctx     context.Context
	logger  *slog.Logger
	catalog *llmgw.Catalog
	egress  llmports.EgressLog
}

func newGateway(ctx context.Context, logger *slog.Logger, store llmports.ModelStore, egress llmports.EgressLog) *gateway {
	return &gateway{ctx: ctx, logger: logger, catalog: llmgw.NewCatalog(store, logger), egress: egress}
}

// configure rebuilds the providers and discovers their models in the background
// (REQ-LLM-002: at start, and again after a reload changed them). The daemon never waits on
// a provider to start or to answer config.reload. With `router.offline = true` no remote
// provider is contacted (REQ-LLM-004).
func (g *gateway) configure(models config.Models, resolved []secdomain.ResolvedCredential) {
	g.catalog.Configure(models.Router.Offline, buildEntries(g.logger, g.egress, models, resolved))
	go func() {
		if err := g.catalog.Refresh(g.ctx); err != nil && g.ctx.Err() == nil {
			g.logger.Warn("model discovery was incomplete", slog.Any("error", err))
		}
	}()
}

// buildEntries turns each configured provider into a catalog entry: an adapter with its
// credential, or no adapter and the reason it may not run. A provider whose credential could
// not be resolved gets no adapter at all, so nothing can call it (REQ-SEC-008).
func buildEntries(logger *slog.Logger, egress llmports.EgressLog, models config.Models, resolved []secdomain.ResolvedCredential) []llmgw.Entry {
	byID := make(map[string]secdomain.ResolvedCredential, len(resolved))
	for _, r := range resolved {
		byID[r.ProviderID] = r
	}
	entries := make([]llmgw.Entry, 0, len(models.Providers))
	for _, p := range models.Providers {
		cred := byID[p.ID]
		e := llmgw.Entry{ID: p.ID, Health: llmdomain.Health(cred.Health), Reason: cred.Reason}
		if cred.Health == secdomain.HealthDown {
			entries = append(entries, e)
			continue
		}
		if e.Health == "" {
			e.Health = llmdomain.HealthUnknown
		}
		provider, err := adapterFor(p, llmdomain.NewAPIKey(cred.Secret.Reveal()), egress)
		switch {
		case err != nil:
			logger.Error("a provider could not be built", slog.String("provider", p.ID), slog.Any("error", err))
			e.Health, e.Reason = llmdomain.HealthDown, llmdomain.ReasonInvalidConfig
		case provider == nil:
			// Nothing can call it, whatever its credential says.
			e.Health, e.Reason = llmdomain.HealthUnknown, llmdomain.ReasonNoAdapter
		default:
			e.Provider = provider
		}
		entries = append(entries, e)
	}
	return entries
}

// adapterFor builds the adapter for a provider type, or nil for a type this build has none
// for yet: `ollama` arrives with T-F1-06, `yzma` later in F1. Every adapter records its
// requests to hosts that are not loopback (Art. 4).
func adapterFor(p config.Provider, key llmdomain.APIKey, egress llmports.EgressLog) (llmports.Provider, error) {
	switch p.Type {
	case "openai-compat", "lmstudio", "llamacpp":
		return openaicompat.New(openaicompat.Config{ID: p.ID, BaseURL: p.BaseURL, APIKey: key, Egress: egress})
	case "openrouter":
		return openrouter.New(openrouter.Config{ID: p.ID, BaseURL: p.BaseURL, APIKey: key, Egress: egress})
	default:
		return nil, nil
	}
}

// List implements api.ModelService.
func (g *gateway) List(ctx context.Context, refresh bool) ([]api.Model, error) {
	models, err := g.catalog.List(ctx, refresh)
	if err != nil {
		return nil, err
	}
	out := make([]api.Model, 0, len(models))
	for _, m := range models {
		out = append(out, api.Model{
			ID: m.ID, Provider: m.Provider, Local: m.Local,
			Caps: api.ModelCaps{
				Tools: m.Caps.Tools, Vision: m.Caps.Vision, Reasoning: m.Caps.Reasoning,
				JSONSchema: m.Caps.JSONSchema, ContextWindow: m.Caps.ContextWindow,
			},
			PriceInMicroUSDPerMTok: m.PriceInMicroUSDPerMTok, PriceOutMicroUSDPerMTok: m.PriceOutMicroUSDPerMTok,
			Health: string(m.Health), Reason: m.Reason,
		})
	}
	return out, nil
}
