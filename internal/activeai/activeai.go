// Package activeai suggests a one-line fix when a user's command fails (REQ-AAI-001). It reads
// block.closed from the bus, asks a local model through its own port and publishes
// block.suggestion; it imports no other module's service (delta `2026-10-active-ai`).
package activeai

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/activeai/domain"
	"github.com/ecrespo/umbral/internal/activeai/ports"
	"github.com/ecrespo/umbral/internal/bus"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// Timeout bounds one suggestion, the block's output read and the model's answer together.
const Timeout = 10 * time.Second

// window is the sliding interval `per_minute` counts over.
const window = time.Minute

// Config is `[active_ai]`, with a clock a test can move.
type Config struct {
	Enabled   bool
	PerMinute int
	Now       func() time.Time
	Logger    *slog.Logger
}

// Service turns failed blocks into suggestions.
type Service struct {
	cfg    Config
	bus    *bus.Bus
	model  ports.Model
	blocks ports.Blocks
	sub    *bus.Subscription

	mu    sync.Mutex
	busy  bool
	asked []time.Time
	wg    sync.WaitGroup
}

// New subscribes a service to block.closed. It returns nil, and subscribes nothing, while
// Active AI is disabled.
func New(cfg Config, b *bus.Bus, model ports.Model, blocks ports.Blocks) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil //nolint:nilnil // a disabled feature has no service and no error
	}
	if b == nil || model == nil || blocks == nil {
		return nil, errors.New("activeai: a bus, a model and a block reader are required")
	}
	if cfg.PerMinute < 1 {
		return nil, errors.New("activeai: per_minute must be at least 1")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{cfg: cfg, bus: b, model: model, blocks: blocks, sub: b.Subscribe(sessports.KindBlockClosed)}, nil
}

// Run handles closed blocks until ctx is cancelled or the bus closes, then waits for the
// suggestion in flight.
func (s *Service) Run(ctx context.Context) {
	defer s.wg.Wait()
	defer s.sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-s.sub.C():
			if !open {
				return
			}
			closed, ok := ev.(sessports.BlockClosed)
			if !ok || !domain.Eligible(closed.Block) || !s.take() {
				continue
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				ev, ok := s.suggest(ctx, closed)
				// Freed before the event is out, so a failure that follows it is not dropped as
				// arriving while busy.
				s.release()
				if ok {
					s.bus.Publish(ev)
				}
			}()
		}
	}
}

// take claims the one suggestion slot and a place in the window, or reports that the failure
// is dropped: a suggestion is in flight, or per_minute were asked in the last minute. A drop
// takes nothing.
func (s *Service) take() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	now := s.cfg.Now()
	kept := s.asked[:0]
	for _, at := range s.asked {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	s.asked = kept
	if len(s.asked) >= s.cfg.PerMinute {
		return false
	}
	s.asked = append(s.asked, now)
	s.busy = true
	return true
}

func (s *Service) release() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

// suggest asks for a block's suggestion and reports the event to publish, if any.
func (s *Service) suggest(ctx context.Context, closed sessports.BlockClosed) (ports.BlockSuggestion, bool) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	b := closed.Block
	output, err := s.blocks.Output(ctx, b.ID, b.SessionID)
	if err != nil {
		s.cfg.Logger.DebugContext(ctx, "activeai: block output unreadable", "block_id", b.ID, "err", err)
		return ports.BlockSuggestion{}, false
	}
	answer, model, err := s.model.Suggest(ctx, domain.System, domain.Prompt(b, output))
	if err != nil {
		s.cfg.Logger.DebugContext(ctx, "activeai: no suggestion", "block_id", b.ID, "err", err)
		return ports.BlockSuggestion{}, false
	}
	text := domain.FirstLine(answer)
	if text == "" {
		s.cfg.Logger.DebugContext(ctx, "activeai: empty suggestion", "block_id", b.ID)
		return ports.BlockSuggestion{}, false
	}
	return ports.BlockSuggestion{BlockID: b.ID, SessionID: b.SessionID, Text: text, Model: model}, true
}
