package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// ModelService is the model catalog as model.list sees it (API Spec §5.26). The daemon's
// composition root adapts llmgw's catalog to it, so the gateway never learns the wire.
type ModelService interface {
	List(ctx context.Context, refresh bool) ([]Model, error)
}

// Model is API Spec §4's Model. `reason` says why `health` is not `ok` — its provider's
// credential state or a failed discovery (delta `2026-09-provider-config`, decision 8).
type Model struct {
	ID                      string    `json:"id"`
	Provider                string    `json:"provider"`
	Local                   bool      `json:"local"`
	Caps                    ModelCaps `json:"caps"`
	PriceInMicroUSDPerMTok  int64     `json:"price_in_micro_usd_per_mtok"`
	PriceOutMicroUSDPerMTok int64     `json:"price_out_micro_usd_per_mtok"`
	Health                  string    `json:"health"`
	Reason                  string    `json:"reason,omitempty"`
}

// ModelCaps is a model's capabilities; context_window is 0 when the provider does not say.
type ModelCaps struct {
	Tools         bool  `json:"tools"`
	Vision        bool  `json:"vision"`
	Reasoning     bool  `json:"reasoning"`
	JSONSchema    bool  `json:"json_schema"`
	ContextWindow int64 `json:"context_window"`
}

type listModelsParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

type modelsResult struct {
	Items []Model `json:"items"`
}

func handleModelList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc := c.server.cfg.Models
	if svc == nil {
		return nil, fmt.Errorf("%w: model.list", ErrNotImplemented)
	}
	var params listModelsParams
	if len(raw) > 0 && string(raw) != jsonNull {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, ValidationError("model.list parameters are not an object")
		}
	}
	items, err := svc.List(ctx, params.Refresh)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []Model{}
	}
	return modelsResult{Items: items}, nil
}

// modelsWired is model.list's `available`.
func modelsWired(cfg Config) bool { return cfg.Models != nil }

// ModelRouter is model.route (API Spec §5.40, REQ-RTR-003): the order the router would try a
// class's candidates for a request of a given shape, and why each was kept or dropped. The
// composition root adapts llmgw's router to it.
type ModelRouter interface {
	Route(ctx context.Context, q ModelRouteParams) (ModelRoute, error)
}

// ModelRouteParams is a class, or one pinned model, and the shape of a request.
type ModelRouteParams struct {
	Class string `json:"class,omitempty"`
	Model string `json:"model,omitempty"`
	// What the request needs from a model; input_tokens is the estimated prompt and response
	// reserve, 0 for none in particular.
	Tools       bool  `json:"tools,omitempty"`
	Vision      bool  `json:"vision,omitempty"`
	JSONSchema  bool  `json:"json_schema,omitempty"`
	Reasoning   bool  `json:"reasoning,omitempty"`
	InputTokens int64 `json:"input_tokens,omitempty"`
}

// ModelRoute is model.route's result: the kept candidates in call order, then the dropped
// ones in declared order.
type ModelRoute struct {
	// Class or Model, whichever was asked.
	Class      string           `json:"class,omitempty"`
	Model      string           `json:"model,omitempty"`
	Policy     string           `json:"policy"`
	Candidates []RouteCandidate `json:"candidates"`
}

// RouteCandidate is one candidate, kept or dropped, with what the policy ordered it by.
type RouteCandidate struct {
	ModelID string `json:"model_id"`
	// Rank is the candidate's place in call order, from 1; null for a dropped one.
	Rank                    *int   `json:"rank"`
	Kept                    bool   `json:"kept"`
	Reason                  string `json:"reason"`
	Provider                string `json:"provider" api:"optional"`
	Local                   bool   `json:"local"`
	Health                  string `json:"health" api:"optional"`
	PriceInMicroUSDPerMTok  int64  `json:"price_in_micro_usd_per_mtok"`
	PriceOutMicroUSDPerMTok int64  `json:"price_out_micro_usd_per_mtok"`
	ContextWindow           int64  `json:"context_window"`
	DeclaredIndex           int    `json:"declared_index"`
}

// routeClasses are the task classes models.toml may declare (Tech Design §5.1).
var routeClasses = map[string]bool{"fast": true, "code": true, "plan": true, "embed": true}

func handleModelRoute(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc := c.server.cfg.ModelRoutes
	if svc == nil {
		return nil, fmt.Errorf("%w: model.route", ErrNotImplemented)
	}
	var p ModelRouteParams
	if err := decode(raw, &p, "model.route"); err != nil {
		return nil, err
	}
	switch {
	case (p.Class == "") == (p.Model == ""):
		return nil, ValidationError("model.route takes a class or a model, not both",
			ErrorField{Field: "class", Issue: "exactly one of class and model"})
	case p.Class != "" && !routeClasses[p.Class]:
		return nil, ValidationError("class is fast, code, plan or embed", ErrorField{Field: "class", Issue: "unknown class"})
	case p.InputTokens < 0:
		return nil, ValidationError("input_tokens is not negative", ErrorField{Field: "input_tokens", Issue: "negative"})
	}
	route, err := svc.Route(ctx, p)
	if err != nil {
		return nil, err
	}
	if route.Candidates == nil {
		route.Candidates = []RouteCandidate{}
	}
	return route, nil
}

// routesWired is model.route's `available`.
func routesWired(cfg Config) bool { return cfg.ModelRoutes != nil }
