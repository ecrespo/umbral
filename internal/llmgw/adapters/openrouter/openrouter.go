// Package openrouter is the adapter for OpenRouter (REQ-LLM-001). Calls go through Fantasy's
// OpenRouter provider, which knows its reasoning and usage extensions; discovery reads
// `<base_url>/models`, which carries each model's prices and parameters (REQ-LLM-002).
package openrouter

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
	fopenrouter "charm.land/fantasy/providers/openrouter"

	"github.com/ecrespo/umbral/internal/llmgw/adapters/fantasyconv"
	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/llmgw/ports"
)

// DefaultBaseURL is OpenRouter's API.
const DefaultBaseURL = fopenrouter.DefaultURL

// Config is one provider entry of models.toml, its credential resolved.
type Config struct {
	ID      string
	BaseURL string
	APIKey  domain.APIKey
	// Egress records every request to a host that is not loopback; without one, such a
	// request is refused (Art. 4).
	Egress ports.EgressLog
	// HTTPClient is used for discovery and calls; nil means a default one.
	HTTPClient *http.Client
}

// Provider is OpenRouter, or a server that speaks its API.
type Provider struct {
	cfg Config
	// client reads the model list at base_url itself; calls go through fp.
	client *http.Client
	fp     fantasy.Provider
}

// Format keeps every verb from printing the provider's insides, where Fantasy holds the key
// as a plain string.
func (p *Provider) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "openrouter provider %q", p.cfg.ID)
}

const discoveryTimeout = 30 * time.Second

// New builds the adapter. Fantasy's provider has OpenRouter's URL built in, so when
// models.toml names another base URL, the HTTP client rewrites requests to it.
func New(cfg Config) (*Provider, error) {
	if cfg.ID == "" {
		return nil, errors.New("openrouter: a provider id is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if err := fantasyconv.CheckBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	// Egress is recorded innermost, after the rewrite, so the log names the host the request
	// really goes to.
	discovery := fantasyconv.Client(cfg.HTTPClient, cfg.ID, cfg.Egress)
	client := discovery
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base != DefaultBaseURL {
		c := *discovery
		c.Transport = rewrite{from: DefaultBaseURL, to: base, next: discovery.Transport}
		client = &c
	}
	opts := []fopenrouter.Option{fopenrouter.WithName(cfg.ID), fopenrouter.WithHTTPClient(client)}
	if !cfg.APIKey.IsZero() {
		opts = append(opts, fopenrouter.WithAPIKey(cfg.APIKey.Reveal()))
	}
	fp, err := fopenrouter.New(opts...)
	if err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg, client: discovery, fp: fp}, nil
}

// ID implements ports.Provider.
func (p *Provider) ID() string { return p.cfg.ID }

// Local implements ports.Provider: OpenRouter is a remote service.
func (p *Provider) Local() bool { return false }

type modelList struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int64  `json:"context_length"`
		Pricing       struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
		Architecture struct {
			InputModalities []string `json:"input_modalities"`
		} `json:"architecture"`
		SupportedParameters []string `json:"supported_parameters"`
	} `json:"data"`
}

// Models implements ports.Provider.
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
		params := m.SupportedParameters
		out = append(out, domain.Model{
			ID: domain.QualifiedID(p.cfg.ID, m.ID), Provider: p.cfg.ID,
			Caps: domain.Capabilities{
				Tools:         slices.Contains(params, "tools"),
				Reasoning:     slices.Contains(params, "reasoning"),
				JSONSchema:    slices.Contains(params, "structured_outputs"),
				Vision:        slices.Contains(m.Architecture.InputModalities, "image"),
				ContextWindow: m.ContextLength,
			},
			PriceInMicroUSDPerMTok:  price(m.Pricing.Prompt),
			PriceOutMicroUSDPerMTok: price(m.Pricing.Completion),
		})
	}
	return out, nil
}

// price reads OpenRouter's USD-per-token string; anything unreadable or negative is 0.
func price(s string) int64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return domain.MicroUSDPerMTok(v)
}

// Stream implements ports.Provider.
func (p *Provider) Stream(ctx context.Context, req domain.Request) (iter.Seq2[domain.Event, error], error) {
	lm, err := p.fp.LanguageModel(ctx, req.Model)
	if err != nil {
		return nil, fantasyconv.Error(p.cfg.ID, err)
	}
	return fantasyconv.Stream(ctx, p.cfg.ID, lm, req)
}

// rewrite sends requests for one base URL to another. Anything else is refused rather than
// passed through: the request carries the key, and a URL Fantasy builds that this adapter
// does not know must not take it to a host models.toml never named.
type rewrite struct {
	from, to string
	next     http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	rest, ok := strings.CutPrefix(u, r.from)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "?")) {
		return nil, fmt.Errorf("openrouter: refusing a request outside %s", r.from)
	}
	out := req.Clone(req.Context())
	target, err := out.URL.Parse(r.to + rest)
	if err != nil {
		return nil, err
	}
	out.URL = target
	out.Host = target.Host
	return r.next.RoundTrip(out)
}
