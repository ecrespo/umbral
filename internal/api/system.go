package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ecrespo/umbral/internal/store"
)

// interactiveClients are the kinds allowed to drive a terminal.
var interactiveClients = []ClientKind{ClientTUI, ClientDesktop}

// registry is the method table. Adding a method here is the only way to expose one, and
// the kinds field is where API Spec §2's per-client-kind allowlist lives.
//
// A module that is not wired in contributes no methods. That is what keeps `capabilities()`
// honest: it is derived from this table, and a daemon that advertised `workspace` while
// every `workspace.*` call answered METHOD_NOT_FOUND would be telling a client to take a
// branch that cannot work — the exact drift the derivation exists to prevent. The nil
// checks inside the handlers stay as well, because a table built once and a config read
// later should not be two sources of the same truth.
func (s *Server) registry() map[string]method {
	table := map[string]method{
		"system.hello":  {handle: handleHello, beforeHello: true},
		"system.status": {handle: handleStatus},

		// session.* is not in the `cli` allowlist of API Spec §2: `umb` reads blocks and
		// talks to threads, it does not drive terminals.
		"session.create": {handle: handleSessionCreate, kinds: interactiveClients},
		"session.list":   {handle: handleSessionList, kinds: interactiveClients},
		"session.input":  {handle: handleSessionInput, kinds: interactiveClients},
		"session.resize": {handle: handleSessionResize, kinds: interactiveClients},
		"session.close":  {handle: handleSessionClose, kinds: interactiveClients},

		"session.subscribe":   {handle: handleSessionSubscribe, kinds: interactiveClients},
		"session.unsubscribe": {handle: handleSessionUnsubscribe, kinds: interactiveClients},

		// block.* carries no kinds: API Spec §2 grants it to every client kind, and `umb`
		// exists mostly to read it.
		"block.list":   {handle: handleBlockList},
		"block.get":    {handle: handleBlockGet},
		"block.search": {handle: handleBlockSearch},
	}

	if s.cfg.Workspaces != nil {
		for name, m := range workspaceMethods() {
			table[name] = m
		}
	}
	return table
}

// workspaceMethods is the `workspace.*`, `tab.*` and `pane.*` surface of API Spec §5.4 to
// §5.6.
func workspaceMethods() map[string]method {
	return map[string]method{
		// The workspace tree is interactive-only for the same reason session.* is: API
		// Spec §2 gives `cli` `system.*`, `block.*`, three `thread.*` and `model.list`,
		// and nothing that arranges windows.
		"workspace.create": {handle: handleWorkspaceCreate, kinds: interactiveClients},
		"workspace.list":   {handle: handleWorkspaceList, kinds: interactiveClients},
		"workspace.focus":  {handle: handleWorkspaceFocus, kinds: interactiveClients},
		"workspace.rename": {handle: handleWorkspaceRename, kinds: interactiveClients},
		"workspace.close":  {handle: handleWorkspaceClose, kinds: interactiveClients},

		"tab.create": {handle: handleTabCreate, kinds: interactiveClients},
		"tab.list":   {handle: handleTabList, kinds: interactiveClients},
		"tab.focus":  {handle: handleTabFocus, kinds: interactiveClients},
		"tab.rename": {handle: handleTabRename, kinds: interactiveClients},
		"tab.close":  {handle: handleTabClose, kinds: interactiveClients},

		"pane.split":  {handle: handlePaneSplit, kinds: interactiveClients},
		"pane.list":   {handle: handlePaneList, kinds: interactiveClients},
		"pane.get":    {handle: handlePaneGet, kinds: interactiveClients},
		"pane.focus":  {handle: handlePaneFocus, kinds: interactiveClients},
		"pane.rename": {handle: handlePaneRename, kinds: interactiveClients},
		"pane.move":   {handle: handlePaneMove, kinds: interactiveClients},
		"pane.close":  {handle: handlePaneClose, kinds: interactiveClients},
	}
}

// handleHello is the handshake of API Spec §2 and the enforcement point for REQ-SEC-003.
//
// Every failure returns UNAUTHORIZED or VALIDATION_ERROR without saying which part was
// wrong beyond what the client needs, and a bad token closes the connection: an attacker
// with a local socket must not get an oracle to grind tokens against.
func handleHello(_ context.Context, c *conn, raw json.RawMessage) (any, error) {
	var params helloParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("system.hello parameters are not an object")
	}

	// A second handshake on an authenticated connection is a protocol violation, and the
	// conservative reading of API Spec §2 is that the connection goes away. Checking it
	// first also means a re-handshake cannot be used to probe tokens on a live session.
	if c.authenticated {
		return nil, fmt.Errorf("%w: this connection already completed the handshake", ErrUnauthorized)
	}

	// The token is checked before anything else. REQ-SEC-003 says a connection that does
	// not present a valid token gets UNAUTHORIZED and is closed, with no exceptions, so
	// no other validation may answer first and leave the connection open.
	if !tokenMatches(c.server.token, params.Token) {
		return nil, fmt.Errorf("%w: invalid token", ErrUnauthorized)
	}

	if params.ProtocolVersion != ProtocolVersion {
		return nil, unsupportedProtocolError(params.ProtocolVersion)
	}

	kind := ClientKind(params.ClientKind)
	if !kind.valid() {
		return nil, ValidationError("unknown client_kind",
			ErrorField{Field: "client_kind", Issue: "must be tui, cli or desktop"})
	}

	c.authenticated = true
	c.clientKind = kind
	c.connectionID = store.NewID(store.PrefixConnection)

	return helloResult{
		DaemonVersion:   c.server.cfg.DaemonVersion,
		ProtocolVersion: ProtocolVersion,
		Capabilities:    c.server.capabilities(),
		ConnectionID:    c.connectionID,
	}, nil
}

// handleStatus answers system.status (API Spec §5.2), which is what `umb status` prints.
func handleStatus(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	status := StatusResult{
		DaemonVersion: c.server.cfg.DaemonVersion,
		UptimeMillis:  time.Since(c.server.startedAt).Milliseconds(),
		Providers:     []ProviderStatus{},
		MCP:           []MCPStatus{},
	}

	if c.server.cfg.Status != nil {
		reported, err := c.server.cfg.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("collect daemon status: %w", err)
		}
		// The caller owns the counts; the daemon owns its own identity and uptime.
		reported.DaemonVersion = status.DaemonVersion
		reported.UptimeMillis = status.UptimeMillis
		if reported.Providers == nil {
			reported.Providers = []ProviderStatus{}
		}
		if reported.MCP == nil {
			reported.MCP = []MCPStatus{}
		}
		status = reported
	}
	return status, nil
}
