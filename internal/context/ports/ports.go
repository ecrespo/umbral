// Package ports is the ports layer of the context module: what the agent runtime reads a
// turn's context through, and the block text the module needs from sessions.
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/context/domain"
)

// Gatherer reads the context of a thread's cwd from the machine.
type Gatherer interface {
	// Rules returns the rules files for cwd, highest precedence first (REQ-CTX-001).
	Rules(ctx context.Context, cwd string) ([]domain.RulesFile, error)
	// Git returns the cwd's git state, or nil when cwd is not inside a repository
	// (REQ-CTX-003).
	Git(ctx context.Context, cwd string) (*domain.GitContext, error)
	// Attach reads one attachment, relative to cwd (REQ-CTX-002, REQ-CTX-005). An attachment
	// that names nothing it can attach is domain.ErrUnknownAttachment.
	Attach(ctx context.Context, cwd string, ref domain.Ref) (domain.Attachment, error)
}

// Blocks is the block history, as far as an `@block` attachment needs it.
type Blocks interface {
	BlockText(ctx context.Context, id string) (domain.BlockText, error)
}
