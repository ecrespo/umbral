package main

import (
	"context"

	"github.com/ecrespo/umbral/internal/agents"
	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/sessions"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/waits"
	waitsports "github.com/ecrespo/umbral/internal/waits/ports"
)

// newWaits builds the wait engine (T-F1-23) over the agent runtime and the terminal: the
// only place the two meet, which is why the adapters below live here.
func newWaits(b *bus.Bus, runtime *agents.Runtime, terminal *sessions.Service, blocks *sessions.Reader) (*waits.Service, error) {
	return waits.New(waits.Config{
		Bus:      b,
		Threads:  threadStatus{runtime: runtime},
		Terminal: screen{sessions: terminal, blocks: blocks},
	})
}

// threadStatus is the runtime as the wait engine reads it.
type threadStatus struct {
	runtime *agents.Runtime
}

func (t threadStatus) Status(ctx context.Context, threadID string) (waitsports.ThreadStatus, error) {
	st, err := t.runtime.Status(ctx, threadID)
	if err != nil {
		return waitsports.ThreadStatus{}, err
	}
	return waitsports.ThreadStatus{State: string(st.State), Attention: st.Attention, TurnID: st.TurnID}, nil
}

func (t threadStatus) TurnEnd(_ context.Context, threadID, turnID string) (string, bool, error) {
	end, ok := t.runtime.TurnEnd(threadID, turnID)
	return end, ok, nil
}

// screen is the terminal as the wait engine reads it: the live screen and the history.
type screen struct {
	sessions *sessions.Service
	blocks   *sessions.Reader
}

func (s screen) ScreenText(ctx context.Context, sessionID string) (waitsports.Screen, error) {
	text, seq, open, err := s.sessions.ScreenText(ctx, sessionID)
	return waitsports.Screen{Text: text, Seq: seq, OpenLine: open}, err
}

func (s screen) Block(ctx context.Context, blockID string) (sessdomain.Block, error) {
	block, _, err := s.blocks.Get(ctx, blockID, "", sessdomain.IncludeNone)
	return block, err
}

func (s screen) BlockText(ctx context.Context, blockID string) (string, error) {
	_, out, err := s.blocks.Get(ctx, blockID, "", sessdomain.IncludePlain)
	return out.Plain, err
}

func (s screen) LatestBlock(ctx context.Context, sessionID string) (sessdomain.Block, bool, error) {
	page, err := s.blocks.List(ctx, sessdomain.BlockFilter{SessionID: sessionID, Limit: 1})
	if err != nil || len(page.Items) == 0 {
		return sessdomain.Block{}, false, err
	}
	return page.Items[0], true, nil
}
