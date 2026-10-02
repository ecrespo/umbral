package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/bus"
	llmports "github.com/ecrespo/umbral/internal/llmgw/ports"
	"github.com/ecrespo/umbral/internal/mcp"
	"github.com/ecrespo/umbral/internal/mcp/adapters/sdk"
	"github.com/ecrespo/umbral/internal/mcp/adapters/serverstore"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/tools/adapters/mcptools"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
)

// newMCP wires the MCP client (T-F1-17): the store over `mcp_servers`, the SDK connector
// recording remote requests in egress_log, the keyring for env_refs, and the catalog that
// puts each server's tools in the registry. It starts connecting the stored servers.
func newMCP(ctx context.Context, logger *slog.Logger, db *store.Store, events *bus.Bus,
	egress llmports.EgressLog, tools *registry.Registry, providers *providerConfig,
) (*mcp.Manager, error) {
	st, err := serverstore.New(db)
	if err != nil {
		return nil, err
	}
	// The catalog calls through the manager it is handed to; m is set before the manager
	// starts, so before any tool is registered.
	var m *mcp.Manager
	call := func(ctx context.Context, server, tool string, args json.RawMessage) (mcpdomain.CallResult, error) {
		return m.Call(ctx, server, tool, args)
	}
	m, err = mcp.New(mcp.Config{
		Store:     st,
		Connector: sdk.New(sdk.Config{Version: buildVersion(), Egress: egress}),
		Secrets:   mcpSecrets{providers: providers},
		Sink:      mcptools.New(tools, call),
		Bus:       events,
		NewID:     store.NewID,
		Redact:    redact,
		Logger:    logger.With(slog.String("module", "mcp")),
	})
	if err != nil {
		return nil, err
	}
	if err := m.Start(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// mcpSecrets resolves env_refs as provider credentials are resolved: the keyring, and the
// environment only under REQ-SEC-012's fallback.
type mcpSecrets struct {
	providers *providerConfig
}

func (s mcpSecrets) Accepts(ctx context.Context, ref mcpdomain.Ref) error {
	if ref.Kind != mcpdomain.RefEnv {
		return nil
	}
	if !s.providers.envFallback(ctx) {
		return fmt.Errorf("%w: env:<VAR> is accepted only where the keyring is unavailable and [secrets] allow_env = true (REQ-SEC-012)",
			mcpdomain.ErrConfigInvalid)
	}
	return nil
}

func (s mcpSecrets) Resolve(ctx context.Context, ref mcpdomain.Ref) (string, string, error) {
	source := secdomain.SourceKeyring
	if ref.Kind == mcpdomain.RefEnv {
		source = secdomain.SourceEnv
	}
	got := s.providers.credential(ctx, secdomain.CredentialRequest{ProviderID: "mcp", Source: source, Ref: ref.Name})
	if got.Secret.IsZero() {
		return "", "", errors.New(got.Reason)
	}
	return got.Secret.Reveal(), got.Reason, nil
}

// withMCP adds the MCP servers to system.status (API Spec §5.2).
func withMCP(status api.StatusFunc, m *mcp.Manager) api.StatusFunc {
	return func(ctx context.Context) (api.StatusResult, error) {
		result, err := status(ctx)
		if err != nil {
			return result, err
		}
		servers, err := m.List(ctx)
		if err != nil {
			return result, err
		}
		result.MCP = make([]api.MCPStatus, 0, len(servers))
		for _, s := range servers {
			result.MCP = append(result.MCP, api.MCPStatus{Name: s.Name, State: string(s.State)})
		}
		return result, nil
	}
}
