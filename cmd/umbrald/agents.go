package main

import (
	"context"
	"iter"
	"log/slog"
	"time"

	"github.com/ecrespo/umbral/internal/agents"
	"github.com/ecrespo/umbral/internal/agents/adapters/threadstore"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/adapters/local"
	"github.com/ecrespo/umbral/internal/context/adapters/skills"
	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	ctxports "github.com/ecrespo/umbral/internal/context/ports"
	"github.com/ecrespo/umbral/internal/llmgw"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	"github.com/ecrespo/umbral/internal/obs"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
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
	tracer   agentsports.Tracer
	// maxCost is the thread cost cap now in force, in micro-USD (REQ-AGT-008).
	maxCost func() int64
	// redact and policy are the rules in force (REQ-SEC-010).
	redact func(string) string
	policy func() secdomain.Policy
	// stallLocal and stallRemote are `[waits]`'s stall windows (REQ-AUT-008); zero takes
	// the runtime's defaults.
	stallLocal, stallRemote time.Duration
	// maxTurns is `[agents] max_concurrent_turns` (REQ-ORC-002).
	maxTurns int
	// skills is the skill store: each turn's catalog and skill_load's reader (T-F1-36).
	skills *skills.Service
	// worktrees is the worktree engine a thread's turns run in (T-F2-03).
	worktrees agentsports.Worktrees
}

// newRuntime wires the agent runtime (T-F1-13) to the modules it reaches through their ports:
// the thread store, the model router, the tool registry and the context gatherer. It returns
// the registry too, where the MCP client puts its servers' tools (T-F1-17).
func newRuntime(ctx context.Context, d agentDeps) (*agents.Runtime, *registry.Registry, error) {
	st, err := threadstore.New(d.db)
	if err != nil {
		return nil, nil, err
	}
	builtins := builtin.Config{
		Fetch:    builtin.FetchConfig{Egress: d.egress, Redact: d.redact},
		Terminal: d.terminal,
	}
	// Set only when there is a store: a nil *skills.Service in an interface is not nil.
	var catalog ctxports.SkillCatalog
	if d.skills != nil {
		builtins.Skills, catalog = d.skills, d.skills
	}
	tools, err := registry.New(builtin.All(builtins)...)
	if err != nil {
		return nil, nil, err
	}
	rt, err := agents.New(ctx, agents.Config{
		Store:   st,
		Models:  routerModels{router: d.gateway.router},
		Tools:   tools,
		Bus:     d.bus,
		Context: local.New(local.Config{Blocks: blockText{reader: d.blocks}}),
		NewID:   store.NewID,
		Metrics: d.metrics,
		Tracer:  d.tracer,
		Logger:  d.logger,
		IsRepo:  isRepo,
		Policy:  d.policy,
		Skills:  catalog,

		Worktrees: d.worktrees,

		MaxCostMicroUSD: d.maxCost,
		StallLocal:      d.stallLocal,
		StallRemote:     d.stallRemote,

		MaxConcurrentTurns: d.maxTurns,
	})
	return rt, tools, err
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

func (m routerModels) Local(ctx context.Context, class, model string) bool {
	return m.router.Local(ctx, class, model)
}
