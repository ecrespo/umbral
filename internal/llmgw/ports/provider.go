package ports

import (
	"context"
	"iter"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// Provider is one configured model provider (docs/ARCHITECTURE.md §6). An adapter turns its
// wire format into the domain's models and normalized events; nothing above it knows which
// API is behind it.
type Provider interface {
	// ID is the provider's id in models.toml.
	ID() string
	// Local reports whether calls stay on this machine (REQ-LLM-004's offline filter).
	Local() bool
	// Models lists what the provider serves (REQ-LLM-002): `/v1/models`, `/api/tags`.
	Models(ctx context.Context) ([]domain.Model, error)
	// Stream starts one call. An error before the first event — a refused request — is
	// returned here; one during the stream is yielded, and ends it.
	Stream(ctx context.Context, req domain.Request) (iter.Seq2[domain.Event, error], error)
}

// ModelStore persists the catalog (Data Model §2.10).
type ModelStore interface {
	// Replace makes provider's models exactly these.
	Replace(ctx context.Context, provider string, models []domain.Model) error
	// SetHealth marks every model of provider.
	SetHealth(ctx context.Context, provider string, health domain.Health) error
	// List returns every stored model, ordered by id.
	List(ctx context.Context) ([]domain.Model, error)
	// Retain deletes the models of every provider not in keep: one removed from models.toml.
	Retain(ctx context.Context, keep []string) error
}

// EgressLog records every request that leaves the machine (Art. 4, REQ-SEC-002).
type EgressLog interface {
	Record(ctx context.Context, rec domain.EgressRecord) error
}
