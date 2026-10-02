// Package ports is the ports layer of the mcp module: where servers are persisted, how a
// session with one is opened, where its tools go, and the events the module publishes.
package ports

import (
	"context"
	"encoding/json"

	"github.com/ecrespo/umbral/internal/bus"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/mcp/domain"
)

// Servers is the module's inbound port, which internal/api serves as mcp.server.* (API §5.27).
type Servers interface {
	List(ctx context.Context) ([]domain.Server, error)
	// Add persists the server and starts connecting it; it returns it `connecting`.
	Add(ctx context.Context, p domain.AddParams) (domain.Server, error)
	// Remove disconnects the server, takes its tools out of the registry and deletes it.
	Remove(ctx context.Context, name string) error
}

// Store persists servers (`mcp_servers`).
type Store interface {
	List(ctx context.Context) ([]domain.Server, error)
	// Insert adds a server; a name already taken is domain.ErrConflict.
	Insert(ctx context.Context, s domain.Server) error
	// Delete removes a server by name; an unknown one is domain.ErrNotFound.
	Delete(ctx context.Context, name string) error
	// SetState records a server's connection state and the reason for it.
	SetState(ctx context.Context, id string, state domain.State, lastError string, now int64) error
}

// Session is an open connection to one server.
type Session interface {
	Tools(ctx context.Context) ([]domain.ToolInfo, error)
	Call(ctx context.Context, tool string, args json.RawMessage) (domain.CallResult, error)
	// Done is closed when the connection ends: the process exited, the stream broke, or
	// Close was called.
	Done() <-chan struct{}
	Close() error
}

// Connector opens sessions. Env is a stdio server's resolved environment, beyond what the
// connector itself passes on.
type Connector interface {
	Connect(ctx context.Context, s domain.Server, env map[string]string) (Session, error)
}

// Secrets resolves an env reference to its value (REQ-SEC-004, REQ-SEC-012).
type Secrets interface {
	// Accepts reports whether a reference may be configured now: `env:<VAR>` only under the
	// keyring fallback. An error is domain.ErrConfigInvalid.
	Accepts(ctx context.Context, ref domain.Ref) error
	// Resolve reads the value, and says why it is degraded when it is — `env_secret` for one
	// read under the keyring fallback (REQ-SEC-012). The error names the reason, never the
	// value.
	Resolve(ctx context.Context, ref domain.Ref) (value, degraded string, err error)
}

// ToolSink is where a connected server's tools go: the tool registry, through
// internal/tools/adapters/mcptools.
type ToolSink interface {
	// Sync makes the registry hold exactly these tools for the server, and returns the ones it
	// could not register, by the server's name for them, with why.
	Sync(s domain.Server, tools []domain.ToolInfo) map[string]string
	// Drop takes every tool of the server out of the registry.
	Drop(server string)
}

// EgressLog records a request to a server off this machine (Art. 4).
type EgressLog interface {
	Record(ctx context.Context, rec llmdomain.EgressRecord) error
}

// Publisher is the daemon's bus.
type Publisher interface {
	Publish(ev bus.Event)
}

// KindServerState is API §6's `mcp.server_state`.
const KindServerState bus.Kind = "mcp.server_state"

// ServerState is a server's state change (REQ-MCP-003).
type ServerState struct {
	Name      string
	State     domain.State
	LastError string
}

// EventKind implements bus.Event.
func (ServerState) EventKind() bus.Kind { return KindServerState }
