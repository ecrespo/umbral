// Package ports is the acp module's published interfaces: what the host needs from the rest
// of the daemon, which cmd/umbrald wires, and what a turn reports through.
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/acp/domain"
)

// Secrets resolves an entry's env reference, `keyring:<path>` or `env:<VAR>`, to its value
// (REQ-SEC-004, REQ-SEC-012). An error names the reason, never the value.
type Secrets interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// PermissionRequest is an agent's `session/request_permission`.
type PermissionRequest struct {
	ThreadID string
	Call     domain.ToolCall
}

// Mediator decides and performs what the agent asks of Umbral (REQ-ACP-003…005). T-F2-07
// hosts the agent with Refuse; T-F2-08 decides with the policy engine.
type Mediator interface {
	// Permission reports whether the call is allowed once. It never allows always.
	Permission(ctx context.Context, req PermissionRequest) (bool, error)
	// ReadFile and WriteFile are `fs/read_text_file` and `fs/write_text_file`.
	ReadFile(ctx context.Context, threadID, path string) (string, error)
	WriteFile(ctx context.Context, threadID, path, content string) error
}

// Sink is where one turn's reports go: the agent runtime, which persists each before it
// publishes it (DD-007). An error from Text or ToolCall is a write that failed; it ends the
// turn.
type Sink interface {
	Text(text string) error
	Thought(text string)
	ToolCall(call domain.ToolCall) error
}
