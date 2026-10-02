package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	mcpports "github.com/ecrespo/umbral/internal/mcp/ports"
)

// McpServer is API Spec §4's McpServer.
type McpServer struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Trust     string   `json:"trust"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	LastError *string  `json:"last_error"`
}

func toWireMcpServer(s mcpdomain.Server) McpServer {
	tools := s.Tools
	if tools == nil {
		tools = []string{}
	}
	return McpServer{
		ID: s.ID, Name: s.Name, Transport: string(s.Transport), Trust: string(s.Trust), State: string(s.State),
		Tools: tools, LastError: emptyAsNull(s.LastError),
	}
}

// mcpServerStatePayload is `mcp.server_state` (API §6, REQ-MCP-003).
type mcpServerStatePayload struct {
	Name      string  `json:"name"`
	State     string  `json:"state"`
	LastError *string `json:"last_error"`
}

func mcpServers(c *conn) (mcpports.Servers, error) {
	if c.server.cfg.MCP == nil {
		return nil, fmt.Errorf("%w: the MCP client is not wired in", ErrNotImplemented)
	}
	return c.server.cfg.MCP, nil
}

func hasMCP(cfg Config) bool { return cfg.MCP != nil }

// mcpMethods is §5.27. §2's cli row does not name them; T-F1-37 adds them with `umb mcp`.
func mcpMethods() map[string]method {
	return map[string]method{
		"mcp.server.list":   {handle: handleMcpList, kinds: interactiveClients, params: emptyResult{}, result: mcpListResult{}},
		"mcp.server.add":    {handle: handleMcpAdd, kinds: interactiveClients, params: mcpAddParams{}, result: McpServer{}},
		"mcp.server.remove": {handle: handleMcpRemove, kinds: interactiveClients, params: mcpRemoveParams{}, result: emptyResult{}},
	}
}

type mcpListResult struct {
	Items []McpServer `json:"items"`
}

func handleMcpList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := mcpServers(c)
	if err != nil {
		return nil, err
	}
	var p emptyResult
	if err := decode(raw, &p, "mcp.server.list"); err != nil {
		return nil, err
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := mcpListResult{Items: make([]McpServer, 0, len(list))}
	for _, s := range list {
		out.Items = append(out.Items, toWireMcpServer(s))
	}
	return out, nil
}

type mcpAddParams struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command" api:"optional"`
	Args      []string          `json:"args" api:"optional"`
	URL       string            `json:"url" api:"optional"`
	EnvRefs   map[string]string `json:"env_refs" api:"optional"`
	Trust     string            `json:"trust" api:"optional"`
}

func handleMcpAdd(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := mcpServers(c)
	if err != nil {
		return nil, err
	}
	var p mcpAddParams
	if err := decode(raw, &p, "mcp.server.add"); err != nil {
		return nil, err
	}
	s, err := svc.Add(ctx, mcpdomain.AddParams{
		Name: p.Name, Transport: mcpdomain.Transport(p.Transport), Command: p.Command, Args: p.Args, URL: p.URL,
		EnvRefs: p.EnvRefs, Trust: mcpdomain.Trust(p.Trust),
	})
	if err != nil {
		return nil, err
	}
	return toWireMcpServer(s), nil
}

type mcpRemoveParams struct {
	Name string `json:"name"`
}

func handleMcpRemove(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := mcpServers(c)
	if err != nil {
		return nil, err
	}
	var p mcpRemoveParams
	if err := decode(raw, &p, "mcp.server.remove"); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, ValidationError("name is required", ErrorField{Field: "name", Issue: requiredTag})
	}
	if err := svc.Remove(ctx, p.Name); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

// mcpDomainError maps the MCP module's sentinels onto §3.
func mcpDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, mcpdomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	case errors.Is(err, mcpdomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, mcpdomain.ErrConfigInvalid):
		return codeConfigInvalid, domainConfigInvalid, true
	case errors.Is(err, mcpdomain.ErrConflict):
		return codeConflict, domainConflict, true
	default:
		return 0, "", false
	}
}
