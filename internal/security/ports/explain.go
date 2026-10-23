package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/security/domain"
)

// ThreadPolicy is what a thread lends policy.explain: its mode and the directory its write
// root is found from.
type ThreadPolicy struct {
	Mode domain.Mode
	Cwd  string
	// WriteRoot is the thread's write root when it is not the one found from Cwd: the thread's
	// active worktree (REQ-WT-002). "" finds it from Cwd.
	WriteRoot string
}

// ExplainSource reads what the agent runtime decides with. It has no write: policy.explain
// is a read-only evaluation (API Spec §5.36).
type ExplainSource interface {
	// ThreadPolicy reads one thread; an unknown one is domain.ErrUnknownThread.
	ThreadPolicy(ctx context.Context, threadID string) (ThreadPolicy, error)
	// Rules are the persisted rules that apply to a thread — its own and the global ones —
	// or the global ones alone for an empty id.
	Rules(ctx context.Context, threadID string) ([]domain.Rule, error)
}

// ToolCatalog says what a registered tool declares and how it shows a path to the policy, so
// an explanation decides on the action a turn would form.
type ToolCatalog interface {
	// Risk is the registered tool's declared risk; false for a tool not registered.
	Risk(tool string) (domain.Risk, bool)
	// PolicyPath is a path argument as the built-in tools hand it to the policy: made
	// absolute against cwd, with its symlinks resolved.
	PolicyPath(cwd, p string) string
}

// Explainer is policy.explain (REQ-SEC-009).
type Explainer interface {
	Explain(ctx context.Context, req domain.ExplainRequest) (domain.Decision, error)
}
