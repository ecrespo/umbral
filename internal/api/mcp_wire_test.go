package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	mcpports "github.com/ecrespo/umbral/internal/mcp/ports"
)

type fakeMCP struct {
	servers []mcpdomain.Server
	added   []mcpdomain.AddParams
	removed []string
}

func (f *fakeMCP) List(context.Context) ([]mcpdomain.Server, error) { return f.servers, nil }

func (f *fakeMCP) Add(_ context.Context, p mcpdomain.AddParams) (mcpdomain.Server, error) {
	p, err := p.Validate()
	if err != nil {
		return mcpdomain.Server{}, err
	}
	f.added = append(f.added, p)
	return mcpdomain.Server{ID: "mcp_1", Name: p.Name, Transport: p.Transport, Trust: p.Trust, State: mcpdomain.StateConnecting}, nil
}

func (f *fakeMCP) Remove(_ context.Context, name string) error {
	if name != "gitlab" {
		return mcpdomain.ErrNotFound
	}
	f.removed = append(f.removed, name)
	return nil
}

// TestMcpServerMethodsReachTheWire_REQ_MCP_002: mcp.server.add/list/remove answer API §4's
// McpServer and map the module's errors onto §3; mcp.server_state reaches clients.
func TestMcpServerMethodsReachTheWire_REQ_MCP_002(t *testing.T) {
	t.Parallel()
	svc := &fakeMCP{servers: []mcpdomain.Server{{
		ID: "mcp_1", Name: "gitlab", Transport: mcpdomain.TransportStdio, Trust: mcpdomain.TrustUntrusted,
		State: mcpdomain.StateConnected, Tools: []string{"get_issue"},
	}}}
	b := bus.New()
	s := testServerWithConfig(t, Config{MCP: svc, Bus: b})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Notify(ctx)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	resp := c.call(2, "mcp.server.list", map[string]any{})
	raw := ordered[mcpListResult](t, resp.Result)
	if resp.Error != nil || string(raw) != `{"items":[{"id":"mcp_1","name":"gitlab","transport":"stdio","trust":"untrusted","state":"connected","tools":["get_issue"],"last_error":null}]}` {
		t.Fatalf("list = %s %+v", raw, resp.Error)
	}
	resp = c.call(3, "mcp.server.add", map[string]any{"name": "files", "transport": "stdio", "command": "mcp-files", "args": []string{"--root", "/w"}, "env_refs": map[string]string{"T": "keyring:umbral/f"}})
	raw = ordered[McpServer](t, resp.Result)
	if resp.Error != nil || string(raw) != `{"id":"mcp_1","name":"files","transport":"stdio","trust":"untrusted","state":"connecting","tools":[],"last_error":null}` {
		t.Fatalf("add = %s %+v", raw, resp.Error)
	}
	if len(svc.added) != 1 || svc.added[0].Args[1] != "/w" || svc.added[0].EnvRefs["T"] != "keyring:umbral/f" {
		t.Fatalf("the module got %+v", svc.added)
	}
	if resp := c.call(4, "mcp.server.add", map[string]any{"name": "x", "transport": "stdio", "command": "c", "env_refs": map[string]string{"T": "plaintext"}}); resp.Error == nil || resp.Error.Code != codeConfigInvalid {
		t.Fatalf("a plaintext env value = %+v, want CONFIG_INVALID", resp.Error)
	}
	if resp := c.call(5, "mcp.server.add", map[string]any{"name": "x", "transport": "sse"}); resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Fatalf("a bad transport = %+v, want VALIDATION_ERROR", resp.Error)
	}
	if resp := c.call(6, "mcp.server.remove", map[string]any{"name": "gitlab"}); resp.Error != nil {
		t.Fatalf("remove = %+v", resp.Error)
	}
	if resp := c.call(7, "mcp.server.remove", map[string]any{"name": "nope"}); resp.Error == nil || resp.Error.Code != codeNotFound {
		t.Fatalf("remove of an unknown server = %+v, want NOT_FOUND", resp.Error)
	}

	b.Publish(mcpports.ServerState{Name: "gitlab", State: mcpdomain.StateUnavailable, LastError: "the connection ended"})
	method, _, n := c.readNotification(t)
	if method != "mcp.server_state" || string(n) != `{"name":"gitlab","state":"unavailable","last_error":"the connection ended"}` {
		t.Fatalf("mcp.server_state = %s", n)
	}
}

// TestUmbCannotManageMcpServersYet_REQ_API_001: §2's cli row does not name mcp.server.*
// until T-F1-37 adds it.
func TestUmbCannotManageMcpServersYet_REQ_API_001(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{MCP: &fakeMCP{}, Bus: bus.New()})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := c.call(2, "mcp.server.list", map[string]any{}); resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("mcp.server.list from umb = %+v", resp.Error)
	}
}

// ordered re-encodes a decoded result through its wire type, so the comparison is in the
// type's field order rather than a map's.
func ordered[T any](t *testing.T, result any) []byte {
	t.Helper()
	raw, _ := json.Marshal(result)
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return out
}
