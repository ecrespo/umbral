package main

import (
	"context"
	"iter"
	"log/slog"
	"testing"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/llmgw"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// listedProvider lists fixed models and is never called.
type listedProvider struct {
	id     string
	local  bool
	models []llmdomain.Model
}

func (p listedProvider) ID() string  { return p.id }
func (p listedProvider) Local() bool { return p.local }
func (p listedProvider) Models(context.Context) ([]llmdomain.Model, error) {
	return append([]llmdomain.Model(nil), p.models...), nil
}

func (p listedProvider) Stream(context.Context, llmdomain.Request) (iter.Seq2[llmdomain.Event, error], error) {
	panic("model.route sent a request")
}

// TestModelRouteExplainsTheOrder_REQ_RTR_003: the daemon's model.route answers the router's own
// plan under the policy models.toml sets — the kept candidates ranked in call order, the
// dropped ones after them with the reason — and sends nothing to any provider.
func TestModelRouteExplainsTheOrder_REQ_RTR_003(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g, err := newGateway(ctx, slog.New(slog.DiscardHandler), &memModelsByProvider{rows: map[string][]llmdomain.Model{}},
		&memEgress{}, nopUsage{}, nopTracer{}, redactBuiltin)
	if err != nil {
		t.Fatal(err)
	}
	// configure applies the [router] table's policy; there are no providers it could build.
	g.configure(config.Models{
		Router:  config.Router{Policy: "local-first", BreakerFailures: 3, BreakerCooldownS: 60},
		Classes: map[string][]string{"code": {"or/big", "or/blind", "ol/small"}},
	}, nil)

	caps := llmdomain.Capabilities{Tools: true, ContextWindow: 128_000}
	blind := caps
	blind.Tools = false
	remote := listedProvider{id: "or", models: []llmdomain.Model{
		{ID: "or/big", Caps: caps, PriceInMicroUSDPerMTok: 2_000_000},
		{ID: "or/blind", Caps: blind},
	}}
	local := listedProvider{id: "ol", local: true, models: []llmdomain.Model{{ID: "ol/small", Local: true, Caps: caps}}}
	g.catalog.Configure(false, []llmgw.Entry{
		{ID: "or", Provider: remote, Health: llmdomain.HealthOK},
		{ID: "ol", Provider: local, Health: llmdomain.HealthOK},
	})
	if err := g.catalog.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	route, err := g.Route(ctx, api.ModelRouteParams{Class: "code", Tools: true})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Policy != "local-first" || route.Class != "code" || len(route.Candidates) != 3 {
		t.Fatalf("route = %+v", route)
	}
	want := []struct {
		id     string
		rank   int
		reason string
	}{{"ol/small", 1, "eligible"}, {"or/big", 2, "eligible"}, {"or/blind", 0, "no_tools"}}
	for i, w := range want {
		c := route.Candidates[i]
		rank := 0
		if c.Rank != nil {
			rank = *c.Rank
		}
		if c.ModelID != w.id || rank != w.rank || c.Reason != w.reason || c.Kept != (w.rank > 0) {
			t.Errorf("candidate %d = %+v, want %s ranked %d (%s)", i, c, w.id, w.rank, w.reason)
		}
	}
	if big := route.Candidates[1]; big.PriceInMicroUSDPerMTok != 2_000_000 || big.DeclaredIndex != 0 || big.Local {
		t.Errorf("or/big carries %+v", big)
	}

	// A class models.toml does not declare has nothing to route; it is not an error.
	empty, err := g.Route(ctx, api.ModelRouteParams{Class: "plan"})
	if err != nil || len(empty.Candidates) != 0 {
		t.Errorf("an undeclared class: %+v, %v", empty, err)
	}
}
