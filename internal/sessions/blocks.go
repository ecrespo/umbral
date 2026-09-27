package sessions

import (
	"context"
	"log/slog"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// chunkFlushBytes is how much raw output is buffered before a chunk is written.
//
// One INSERT per PTY read would put a database round trip in the path of every 32 KiB a
// build prints, and compressing 32 KiB at a time throws away the ratio zstd gets from a
// longer window. The buffer is also flushed whenever a block changes state or ends, so no
// output waits on more output to arrive.
const chunkFlushBytes = 64 << 10

// recordOutput turns one chunk of PTY output into blocks (REQ-BLK-001 … REQ-BLK-004).
//
// It runs on the drain goroutine, after the chunk has already been published, so a slow
// database delays persistence and never the live stream a person is watching.
func (s *Service) recordOutput(live *liveSession, chunk []byte) {
	if live.recorder == nil {
		return
	}

	events := live.scanner.Scan(chunk)
	// A pane restored with a stored command shows it once the shell is ready to be typed
	// into, which is the first prompt that ends (REQ-TERM-011).
	s.notePrompt(live, events)

	actions := live.recorder.Feed(events)
	for _, action := range actions {
		s.applyBlockAction(live, action)
	}
	s.noteIntegration(live)
}

// applyBlockAction carries out one instruction from the recorder.
//
// No event is published for a write that failed. DD-007 is "persist before notifying", and
// a notification for a row that is not there is worse than silence: a client would render a
// block it can never fetch. The failure is logged, and the row a later `block.list` returns
// is the one the daemon actually has.
func (s *Service) applyBlockAction(live *liveSession, action domain.Action) {
	switch action.Kind {
	case domain.ActionOpen:
		live.chunkBlockID = ""
		live.chunkSeq = 0
		live.pendingRaw = live.pendingRaw[:0]

		// Persist before notifying (DD-007): a client that reacts to block.started must
		// find the row already there. The chunk target is set only once the row exists,
		// so a failed insert loses the output rather than producing a stream of foreign
		// key errors against a block that was never created.
		if err := s.cfg.Blocks.Create(live.ctx, action.Block); err != nil {
			s.logBlock(live, "create the block", err)
			return
		}
		live.chunkBlockID = action.Block.ID
		s.cfg.Bus.Publish(ports.BlockStarted{Block: action.Block})

	case domain.ActionOutput:
		// Action.Data aliases the scanner's view of the PTY buffer, which the next read
		// overwrites, so this append is the copy that makes it ours.
		live.pendingRaw = append(live.pendingRaw, action.Data...)
		if len(live.pendingRaw) >= chunkFlushBytes {
			s.flushChunk(live)
		}

	case domain.ActionState:
		s.flushChunk(live)
		if err := s.cfg.Blocks.SetState(live.ctx, action.Block.ID, action.Block.State); err != nil {
			s.logBlock(live, "update the block state", err)
			return
		}
		s.cfg.Bus.Publish(ports.BlockUpdated{BlockID: action.Block.ID, State: action.Block.State})

	case domain.ActionClose:
		s.flushChunk(live)
		live.chunkBlockID = ""
		if err := s.cfg.Blocks.Finish(live.ctx, action.Block, action.Plain); err != nil {
			s.logBlock(live, "close the block", err)
			return
		}
		s.cfg.Bus.Publish(ports.BlockClosed{Block: action.Block})
	}
}

// flushChunk writes whatever raw output has accumulated.
func (s *Service) flushChunk(live *liveSession) {
	if len(live.pendingRaw) == 0 || live.chunkBlockID == "" {
		return
	}
	if err := s.cfg.Blocks.AppendChunk(live.ctx, live.chunkBlockID, live.chunkSeq, live.pendingRaw); err != nil {
		s.logBlock(live, "store a chunk of output", err)
	}
	live.chunkSeq++
	live.pendingRaw = live.pendingRaw[:0]
}

// noteIntegration promotes a session to `osc133` the first time a marker arrives
// (REQ-BLK-003).
func (s *Service) noteIntegration(live *liveSession) {
	if !live.recorder.SawMarker() {
		return
	}
	s.setIntegration(live, domain.IntegrationOSC133)
}

// startIntegrationTimer gives the session five seconds to prove it has shell integration
// (REQ-BLK-003).
//
// The clock starts when the PTY does, not when the first byte arrives: a shell that never
// prints anything is exactly the case this has to catch, and waiting for output that never
// comes would leave the session `pending` forever.
func (s *Service) startIntegrationTimer(live *liveSession) {
	live.integrationTimer = time.AfterFunc(domain.IntegrationWindow, func() {
		s.setIntegration(live, domain.IntegrationNone)
	})
}

// setIntegration records a new integration state.
//
// Two callers race: the timer fires on its own goroutine while the drain goroutine may be
// reading the first marker, so the check and the write are under the same lock.
//
// The transitions are not symmetric. `none` is a verdict on silence and may only be
// reached from `pending`, because a session that has already spoken is not silent. But a
// marker arriving after the five-second window promotes `none` to `osc133`: the window is
// a heuristic about slow shells, and a session whose blocks are being recorded must not go
// on advertising that it has no integration.
func (s *Service) setIntegration(live *liveSession, integration domain.Integration) {
	live.mu.Lock()
	current := live.session.Integration
	promote := integration == domain.IntegrationOSC133 && current == domain.IntegrationNone
	if current != domain.IntegrationPending && !promote {
		live.mu.Unlock()
		return
	}
	live.session.Integration = integration
	sessionID := live.session.ID
	live.mu.Unlock()

	if _, err := s.cfg.Store.DB().ExecContext(live.ctx,
		"UPDATE sessions SET integration = ? WHERE id = ?", string(integration), sessionID); err != nil {
		s.cfg.Logger.Error("persist the session integration",
			slog.String("session_id", sessionID), slog.Any("error", err))
	}
	s.cfg.Bus.Publish(ports.SessionIntegration{SessionID: sessionID, Integration: integration})
}

// abandonOpenBlock closes the block the session died under (API Spec §7).
func (s *Service) abandonOpenBlock(live *liveSession) {
	if live.recorder == nil {
		return
	}
	for _, action := range live.recorder.Abandon() {
		s.applyBlockAction(live, action)
	}
}

func (s *Service) logBlock(live *liveSession, what string, err error) {
	s.cfg.Logger.Error("block: "+what,
		slog.String("session_id", live.session.ID), slog.Any("error", err))
}

// blockContext is the context every block write uses. It is the session's own, not the
// request's: a block outlives the call that started the command.
func blockContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
