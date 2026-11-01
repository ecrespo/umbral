// Package ports is the ports layer of the plugins module: the store the API serves, the
// modules a plugin's contributions are installed through, and the event it publishes.
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/plugins/domain"
)

// KindPluginChanged is `plugin.changed` (API §6, delta `2026-10-plugins`).
const KindPluginChanged bus.Kind = "plugin.changed"

// PluginChanged announces an installed or removed plugin, published once it is stored
// (DD-007). Change is domain.ChangeInstalled or domain.ChangeRemoved.
type PluginChanged struct {
	Name   string
	Change string
}

// EventKind implements bus.Event.
func (PluginChanged) EventKind() bus.Kind { return KindPluginChanged }

// Plugins is the plugin store as the API serves it, `plugin.*` (REQ-PLG-001). Errors are
// domain.ErrValidation, ErrConflict and ErrNotFound.
type Plugins interface {
	// Inspect checks the bundle at path and returns its manifest and digest, writing nothing.
	Inspect(ctx context.Context, path string) (domain.Inspection, error)
	// Install checks the bundle again and installs it if its digest is still the expected one,
	// with its MCP servers untrusted and its skills, or nothing at all.
	Install(ctx context.Context, path, expectedSHA256 string) (domain.Plugin, error)
	// List is every installed plugin by name.
	List(ctx context.Context) ([]domain.Plugin, error)
	// Get is one installed plugin.
	Get(ctx context.Context, name string) (domain.Plugin, error)
	// Remove deletes the plugin's servers that are still untrusted and its skills that still
	// hold its digest, then its row and its copy.
	Remove(ctx context.Context, name string) error
}

// Servers is the MCP module a plugin's servers are added through (mcp.server.*). Check refuses
// what Add would for the request alone, writing nothing, so inspect can refuse it first.
type Servers interface {
	Check(ctx context.Context, p mcpdomain.AddParams) error
	List(ctx context.Context) ([]mcpdomain.Server, error)
	Add(ctx context.Context, p mcpdomain.AddParams) (mcpdomain.Server, error)
	Remove(ctx context.Context, name string) error
}

// Skills is the skill store a plugin's skills are installed through (skill.*).
type Skills interface {
	List(ctx context.Context) ([]ctxdomain.Skill, error)
	Install(ctx context.Context, p ctxdomain.InstallParams) (ctxdomain.Skill, error)
	Remove(ctx context.Context, name string) error
}

// Bundles reads a bundle's files, with root as the file its root must hold, and the digest
// over them: the skill store's reader, with every limit and refusal it applies.
type Bundles interface {
	ReadFiles(path, root string) (map[string][]byte, error)
}

// Detection checks one agent detection manifest's bytes, without loading it.
type Detection interface {
	Check(raw []byte) error
}

// Publisher is the daemon's bus.
type Publisher interface {
	Publish(ev bus.Event)
}
