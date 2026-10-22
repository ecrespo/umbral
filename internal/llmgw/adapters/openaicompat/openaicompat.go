// Package openaicompat is the adapter for every provider that speaks OpenAI's chat
// completions API: `openai-compat`, `lmstudio` and `llamacpp` (REQ-LLM-001). Calls go through
// Fantasy's openaicompat provider; discovery reads `<base_url>/models` (REQ-LLM-002).
package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	"charm.land/fantasy"
	fopenaicompat "charm.land/fantasy/providers/openaicompat"

	"github.com/ecrespo/umbral/internal/llmgw/adapters/fantasyconv"
	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/llmgw/ports"
)

// Config is one provider entry of models.toml, its credential resolved.
type Config struct {
	ID      string
	BaseURL string
	// APIKey is the resolved credential; zero for a server that needs none.
	APIKey domain.APIKey
	// Egress records every request to a host that is not loopback; without one, such a
	// request is refused (Art. 4).
	Egress ports.EgressLog
	// HTTPClient is used for discovery and calls; nil means a default one.
	HTTPClient *http.Client
}

// Provider is an OpenAI-compatible endpoint.
type Provider struct {
	cfg    Config
	local  bool
	client *http.Client
	fp     fantasy.Provider
}

// Format keeps every verb from printing the provider's insides, where Fantasy holds the key
// as a plain string.
func (p *Provider) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "openai-compat provider %q", p.cfg.ID)
}

// discoveryTimeout bounds a model list; a provider that is up answers it at once.
const discoveryTimeout = 15 * time.Second

// New builds the adapter. It does not contact the server.
func New(cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, errors.New("openaicompat: a provider id is required")
	}
	if err := fantasyconv.CheckBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	client := fantasyconv.Client(cfg.HTTPClient, cfg.ID, cfg.Egress)
	opts := []fopenaicompat.Option{
		fopenaicompat.WithName(cfg.ID),
		fopenaicompat.WithBaseURL(cfg.BaseURL),
		fopenaicompat.WithHTTPClient(client),
	}
	if !cfg.APIKey.IsZero() {
		opts = append(opts, fopenaicompat.WithAPIKey(cfg.APIKey.Reveal()))
	}
	fp, err := fopenaicompat.New(opts...)
	if err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg, local: fantasyconv.IsLocal(cfg.BaseURL), client: client, fp: fp}, nil
}

// ID implements ports.Provider.
func (p *Provider) ID() string { return p.cfg.ID }

// Local implements ports.Provider.
func (p *Provider) Local() bool { return p.local }

// modelList is `/v1/models`. Servers add their own fields for the context window: some
// `context_length`, llama.cpp `meta.n_ctx_train`.
type modelList struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int64  `json:"context_length"`
		Meta          struct {
			NCtxTrain int64 `json:"n_ctx_train"`
		} `json:"meta"`
	} `json:"data"`
}

// Models implements ports.Provider. The chat completions API takes tools, so every model is
// offered with them; one that cannot use them says so when called.
func (p *Provider) Models(ctx context.Context) ([]domain.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	var list modelList
	url := strings.TrimSuffix(p.cfg.BaseURL, "/") + "/models"
	if err := fantasyconv.GetJSON(ctx, p.client, p.cfg.ID, url, p.cfg.APIKey.Reveal(), &list); err != nil {
		return nil, err
	}
	out := make([]domain.Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" {
			continue
		}
		window := m.ContextLength
		if window == 0 {
			window = m.Meta.NCtxTrain
		}
		out = append(out, domain.Model{
			ID: domain.QualifiedID(p.cfg.ID, m.ID), Provider: p.cfg.ID, Local: p.local,
			Caps: domain.Capabilities{Tools: true, ContextWindow: window},
		})
	}
	return out, nil
}

// Stream implements ports.Provider.
func (p *Provider) Stream(ctx context.Context, req domain.Request) (iter.Seq2[domain.Event, error], error) {
	lm, err := p.fp.LanguageModel(ctx, req.Model)
	if err != nil {
		return nil, fantasyconv.Error(p.cfg.ID, err)
	}
	return fantasyconv.Stream(ctx, p.cfg.ID, lm, req)
}
