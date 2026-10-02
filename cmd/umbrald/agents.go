package main

import (
	"context"
	"iter"
	"log/slog"

	"github.com/ecrespo/umbral/internal/agents"
	"github.com/ecrespo/umbral/internal/agents/adapters/threadstore"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/adapters/local"
	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	"github.com/ecrespo/umbral/internal/llmgw"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	"github.com/ecrespo/umbral/internal/obs"
	"github.com/ecrespo/umbral/internal/sessions"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
)

// agentDeps is what the agent runtime is built from.
type agentDeps struct {
	logger   *slog.Logger
	db       *store.Store
	bus      *bus.Bus
	gateway  *gateway
	egress   llmports.EgressLog
	terminal sessports.AgentTerminal
	blocks   *sessions.Reader
	metrics  *obs.Metrics
}

// newRuntime wires the agent runtime (T-F1-13) to the modules it reaches through their ports:
// the thread store, the model router, the built-in tools and the context gatherer.
func newRuntime(ctx context.Context, d agentDeps) (*agents.Runtime, error) {
	st, err := threadstore.New(d.db)
	if err != nil {
		return nil, err
	}
	tools, err := registry.New(builtin.All(builtin.Config{
		Fetch:    builtin.FetchConfig{Egress: d.egress, Redact: redact},
		Terminal: d.terminal,
	})...)
	if err != nil {
		return nil, err
	}
	return agents.New(ctx, agents.Config{
		Store:   st,
		Models:  routerModels{router: d.gateway.router},
		Tools:   tools,
		Bus:     d.bus,
		Context: local.New(local.Config{Blocks: blockText{reader: d.blocks}}),
		NewID:   store.NewID,
		Metrics: d.metrics,
		Logger:  d.logger,
	})
}

// routerModels is the runtime's model gateway: the router, a thread's model or class.
type routerModels struct {
	router *llmgw.Router
}

func (m routerModels) Stream(ctx context.Context, call agentsports.ModelCall) (iter.Seq2[llmdomain.Event, error], error) {
	return m.router.Stream(ctx, llmgw.Call{
		ThreadID: call.ThreadID, TurnID: call.TurnID, Class: call.Class, Model: call.Model, Request: call.Request,
	})
}

func (m routerModels) Window(ctx context.Context, class, model string) int64 {
	return m.router.Window(ctx, class, model)
}

// blockText is the context module's view of the block history, for `@block` attachments.
type blockText struct {
	reader *sessions.Reader
}

func (b blockText) BlockText(ctx context.Context, id string) (ctxdomain.BlockText, error) {
	block, out, err := b.reader.Get(ctx, id, "", sessdomain.IncludePlain)
	if err != nil {
		return ctxdomain.BlockText{}, err
	}
	return ctxdomain.BlockText{ID: block.ID, Command: block.Command, ExitCode: block.ExitCode, Plain: out.Plain}, nil
}

func (m routerModels) Available(ctx context.Context, class, model string) bool {
	return m.router.Available(ctx, class, model)
}
