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

// Files performs the file requests a turn allowed, on the target it decided on (REQ-ACP-004).
type Files interface {
	// Read returns target's text, or limit of its lines from line (1-based; 0 is unset).
	Read(target string, line, limit int) (string, error)
	// Write replaces target's content.
	Write(target, content string) error
}

// Terminals runs a terminal a turn allowed in a new session the thread owns (REQ-ACP-005).
type Terminals interface {
	// Start types command into a new session of the thread's in cwd, and returns once the
	// shell has it.
	Start(ctx context.Context, threadID, cwd, command string) (Terminal, error)
}

// Terminal is one running or ended terminal.
type Terminal interface {
	// Output is what the command printed so far and, once it ended, how.
	Output() (string, *domain.TerminalExit)
	// Wait waits for the command to end.
	Wait(ctx context.Context) (domain.TerminalExit, error)
	// Kill stops the command and keeps the session.
	Kill()
	// Release kills a running command and closes the session.
	Release(ctx context.Context) error
}

// Sink is where one turn's reports go: the agent runtime, which persists each before it
// publishes it (DD-007). An error from Text or ToolCall is a write that failed; it ends the
// turn.
type Sink interface {
	Text(text string) error
	Thought(text string)
	ToolCall(call domain.ToolCall) error
	// Permission decides the agent's `session/request_permission` for call: allowed once, or
	// not at all (REQ-ACP-003).
	Permission(ctx context.Context, call domain.ToolCall) (bool, error)
	// Request decides one of the agent's requests and records it; allowed, it runs perform
	// with the target the decision was made on (REQ-ACP-004, REQ-ACP-005). A request not
	// allowed is an error, and perform is not run.
	Request(ctx context.Context, req domain.Request, perform func(ctx context.Context, target string) (string, error)) error
}
