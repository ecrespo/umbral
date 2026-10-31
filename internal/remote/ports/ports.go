// Package ports is the ports layer of the remote module: where hosts are kept, how a host's
// daemon is reached, and the event the module publishes.
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/remote/domain"
)

// Status is a host as `remote.list` answers it (API §4 RemoteHost).
type Status struct {
	Host      domain.Host
	State     domain.State
	LastError string
}

// Link is how the relay reaches a connected host's daemon: the local end of the forwarded
// socket and the token its daemon expects.
type Link struct {
	Socket string
	Token  string
}

// Hosts is the module's inbound port, which internal/api serves as remote.* and relays through.
type Hosts interface {
	List(ctx context.Context) ([]Status, error)
	// Add persists the host and starts connecting it; it returns it `connecting`.
	Add(ctx context.Context, name, url string) (Status, error)
	// Remove disconnects the host and deletes it.
	Remove(ctx context.Context, name string) error
	// Known reports whether a host of this name is configured, connected or not.
	Known(name string) bool
	// Link reports a connected host's link; false when the host is unknown or not connected.
	Link(name string) (Link, bool)
}

// Store persists hosts (`remotes`).
type Store interface {
	List(ctx context.Context) ([]domain.Host, error)
	// Insert adds a host; a name already taken is domain.ErrConflict.
	Insert(ctx context.Context, h domain.Host) error
	// Delete removes a host by name; an unknown one is domain.ErrNotFound.
	Delete(ctx context.Context, name string) error
}

// Tunnels reaches a host over ssh.
type Tunnels interface {
	// Endpoint asks the host's daemon, started if need be, for its socket and token.
	Endpoint(ctx context.Context, t domain.Target) (domain.Endpoint, error)
	// Forward forwards the local Unix socket to the remote one and returns once the local end
	// accepts connections. The forward ends with ctx, with Close or when ssh exits.
	Forward(ctx context.Context, t domain.Target, local, remote string) (Forward, error)
}

// Forward is one running socket forward.
type Forward interface {
	// Done is closed when the forward has ended.
	Done() <-chan struct{}
	// Err says why it ended, once Done is closed.
	Err() error
	Close() error
}

// Publisher is the daemon's bus.
type Publisher interface {
	Publish(ev bus.Event)
}

// KindHostState is API §6's `remote.state_changed`.
const KindHostState bus.Kind = "remote.state_changed"

// HostState is a host's link state change (REQ-RMT-002).
type HostState struct {
	Name      string
	State     domain.State
	LastError string
}

// EventKind implements bus.Event.
func (HostState) EventKind() bus.Kind { return KindHostState }
