package main

import (
	"context"
	"log/slog"

	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations"
	"github.com/ecrespo/umbral/internal/integrations/adapters/reportstore"
	intports "github.com/ecrespo/umbral/internal/integrations/ports"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// stateEvents are the events after which a pane's effective state may have moved without a
// report: Umbral's own detection (DD-012). Every one is published after its row is written
// (DD-007), so the refresh they trigger reads the new state.
var stateEvents = []bus.Kind{
	sessports.KindBlockStarted,
	sessports.KindBlockUpdated,
	sessports.KindBlockClosed,
	sessports.KindSessionIntegration,
	sessports.KindSessionExited,
	agentsports.KindAttentionChanged,
}

// newIntegrations builds the integration surface (T-F1-25) over the workspace tree, which
// resolves each pane's state, and starts following the events of the authorities it may not
// import. It refreshes once before returning, so what it remembers as told starts as the
// tree restored, before any client connects.
func newIntegrations(ctx context.Context, db *store.Store, b *bus.Bus, panes intports.Panes, logger *slog.Logger) (*integrations.Service, error) {
	reports, err := reportstore.New(db)
	if err != nil {
		return nil, err
	}
	service, err := integrations.New(integrations.Config{
		Store: reports, Metadata: reports, Panes: panes, Bus: b, Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	sub := b.SubscribeBuffered(bus.DefaultBuffer, stateEvents...)
	if err := service.Refresh(ctx); err != nil {
		sub.Close()
		return nil, err
	}
	go func() {
		defer sub.Close()
		service.Follow(ctx, sub)
	}()
	return service, nil
}
