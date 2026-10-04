package main

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/api"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/obs"
	"github.com/ecrespo/umbral/internal/waits"
)

// agentTracer is the agent runtime's Tracer: obs's turn and tool spans (REQ-OBS-001).
type agentTracer struct{ t *obs.Telemetry }

func (a agentTracer) Turn(ctx context.Context, threadID, turnID string) (context.Context, func(ports.TurnTrace)) {
	ctx, end := a.t.Turn(ctx, threadID, turnID)
	return ctx, func(tt ports.TurnTrace) {
		end(obs.TurnEnd{
			StopReason: string(tt.StopReason), Model: tt.Model,
			InTokens: tt.Usage.InTokens, OutTokens: tt.Usage.OutTokens, CostMicroUSD: tt.Usage.CostMicroUSD,
		})
	}
}

func (a agentTracer) Tool(ctx context.Context, callID, tool string) (context.Context, func(ports.ToolTrace)) {
	ctx, end := a.t.Tool(ctx, obs.ToolCall{ID: callID, Name: tool})
	return ctx, func(tt ports.ToolTrace) { end(obs.ToolEnd{Status: string(tt.Status), Risk: tt.Risk}) }
}

// modelTracer is the router's Tracer: one `llm.call` span per usage row.
type modelTracer struct{ t *obs.Telemetry }

func (m modelTracer) ModelCall(ctx context.Context, rec llmdomain.UsageRecord, start, end time.Time) {
	m.t.ModelCall(ctx, obs.ModelCall{
		Provider: rec.Provider, Model: strings.TrimPrefix(rec.ModelID, rec.Provider+"/"), Status: rec.Status,
		InTokens: rec.InTokens, OutTokens: rec.OutTokens, CostMicroUSD: rec.CostMicroUSD,
		FirstTokenMS: rec.FirstTokenMS, Start: start, End: end,
	})
}

// liveGauges are the metrics read from modules built after the telemetry: the wait engine
// and the socket. Until each exists its gauge reads zero.
type liveGauges struct {
	waits  atomic.Pointer[waits.Service]
	server atomic.Pointer[api.Server]
}

func (g *liveGauges) waitsActive() int64 {
	if w := g.waits.Load(); w != nil {
		return w.Active()
	}
	return 0
}

func (g *liveGauges) framesRefused() (in, out uint64) {
	if s := g.server.Load(); s != nil {
		f := s.Frames()
		return uint64(max(f.RefusedIn, 0)), uint64(max(f.RefusedOut, 0))
	}
	return 0, 0
}
