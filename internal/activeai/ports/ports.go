// Package ports is Active AI's published surface: the model and block reader it is given,
// and the event it publishes (delta `2026-10-active-ai`, decision 7).
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
)

// Model asks a local model for a suggestion. An implementation must never send the request
// to a remote provider (REQ-AAI-003); it answers an error when no local model can take it.
type Model interface {
	Suggest(ctx context.Context, system, user string) (text, model string, err error)
}

// Blocks reads a block's plain output.
type Blocks interface {
	Output(ctx context.Context, blockID, sessionID string) (string, error)
}

// KindBlockSuggestion reports a one-line suggestion for a failed block (REQ-AAI-001).
const KindBlockSuggestion bus.Kind = "block.suggestion"

// BlockSuggestion is the event published for a failed block.
type BlockSuggestion struct {
	BlockID   string
	SessionID string
	Text      string
	Model     string
}

// EventKind implements bus.Event.
func (BlockSuggestion) EventKind() bus.Kind { return KindBlockSuggestion }
