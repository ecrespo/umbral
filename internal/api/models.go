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
