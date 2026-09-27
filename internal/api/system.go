package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ecrespo/umbral/internal/store"
)

// methodHello is the handshake, the one method the connection code names: it is the only
// call reachable unauthenticated and the only one that must carry an id (REQ-SEC-018).
const methodHello = "system.hello"

// interactiveClients are the kinds allowed to drive a terminal.
var interactiveClients = []ClientKind{ClientTUI, ClientDesktop}

// treeClients are the kinds allowed to arrange the workspace tree: the interactive ones, and
// `umb`, whose `workspace`/`tab`/`pane`/`layout` commands are REQ-CLI-005 and REQ-CLI-006
// (API Spec §2, delta `2026-09-cli-allowlist`).
var treeClients = []ClientKind{ClientTUI, ClientDesktop, ClientCLI}

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
		methodHello:     {handle: handleHello, beforeHello: true, params: helloParams{}, result: helloResult{}},
		"system.status": {handle: handleStatus, params: emptyResult{}, result: StatusResult{}},

		// The configuration and its providers (API Spec §5.28). Interactive clients only:
		// §2 gives `umb` limits.* and not config.*.
		"config.get": {
			handle: handleConfigGet, kinds: interactiveClients, available: configWired,
			params: emptyResult{}, result: ConfigView{},
		},
		"config.reload": {
			handle: handleConfigReload, kinds: interactiveClients, available: configWired,
			params: emptyResult{}, result: ConfigView{},
		},

		// The model catalog (API Spec §5.26); `umb` may list it too (§2).
		"model.list": {
			handle: handleModelList, available: modelsWired,
			params: listModelsParams{}, result: modelsResult{},
		},

		// The frame limit's two narrow methods (REQ-OBS-005, REQ-CLI-007). Every client kind
		// may call them, `umb` included: raising the limit is what its hint tells a user to do.
		"limits.get": {handle: handleLimitsGet, params: emptyResult{}, result: limitsResult{}},
		"limits.set": {
			handle: handleLimitsSet, available: settingsWritable,
			params: setLimitsParams{}, result: setLimitsResult{},
		},

		// api.schema prints the protocol this binary was built with (API Spec §5.37).
		// It needs no module behind it and is granted to every client kind: a client
		// that cannot ask what the daemon speaks has to guess.
		"api.schema": {handle: handleAPISchema, params: emptyResult{}, result: schemaResult{}},

		// session.* is not in the `cli` allowlist of API Spec §2: `umb` reads blocks and
		// talks to threads, it does not drive terminals.
		"session.create": {handle: handleSessionCreate, kinds: interactiveClients, available: hasSessions, params: createSessionParams{}, result: Session{}},
		"session.list":   {handle: handleSessionList, kinds: interactiveClients, available: hasSessions, params: emptyResult{}, result: sessionListResult{}},
		"session.input":  {handle: handleSessionInput, kinds: interactiveClients, available: hasSessions, params: sessionInputParams{}, result: emptyResult{}},
		"session.resize": {handle: handleSessionResize, kinds: interactiveClients, available: hasSessions, params: sessionResizeParams{}, result: emptyResult{}},
		"session.close":  {handle: handleSessionClose, kinds: interactiveClients, available: hasSessions, params: sessionIDParams{}, result: emptyResult{}},

		// session.snapshot bootstraps a client's own cache of the tree (API Spec §5.3).
		"session.snapshot": {handle: handleSessionSnapshot, kinds: interactiveClients, available: hasWorkspaces, params: emptyResult{}, result: snapshotResult{}},

		"session.subscribe":   {handle: handleSessionSubscribe, kinds: interactiveClients, available: hasSessions, params: sessionSubscribeParams{}, result: sessionSubscribeResult{}},
		"session.unsubscribe": {handle: handleSessionUnsubscribe, kinds: interactiveClients, available: hasSessions, params: sessionIDParams{}, result: emptyResult{}},

		// block.* carries no kinds: API Spec §2 grants it to every client kind, and `umb`
		// exists mostly to read it.
		"block.list":   {handle: handleBlockList, available: hasBlocks, params: listBlocksParams{}, result: blockPage{}},
		"block.get":    {handle: handleBlockGet, available: hasBlocks, params: getBlockParams{}, result: blockResult{}},
		"block.search": {handle: handleBlockSearch, available: hasBlocks, params: searchBlocksParams{}, result: searchPage{}},
	}

	for name, m := range workspaceMethods() {
		m.available = hasWorkspaces
		table[name] = m
	}
	return table
}

// The three modules a method can depend on. A method whose module is absent keeps its name
// in the table and answers NOT_IMPLEMENTED (REQ-API-003, API Spec §9); `capabilities()`
// leaves its namespace out of the handshake, so a client is told what works before it asks.
func hasSessions(cfg Config) bool   { return cfg.Sessions != nil }
func hasBlocks(cfg Config) bool     { return cfg.Blocks != nil }
func hasWorkspaces(cfg Config) bool { return cfg.Workspaces != nil }

// workspaceMethods is the `workspace.*`, `tab.*` and `pane.*` surface of API Spec §5.4 to
// §5.6.
func workspaceMethods() map[string]method {
	return map[string]method{
		// API Spec §2 gives `cli` the tree, because `umb` addresses it (REQ-CLI-005), but
		// not `pane.move`, which has no `umb` verb, and not session.*: `umb` arranges panes
		// and never drives the PTY inside one.
		"workspace.create": {handle: handleWorkspaceCreate, kinds: treeClients, params: createWorkspaceParams{}, result: workspaceCreateResult{}},
		"workspace.list":   {handle: handleWorkspaceList, kinds: treeClients, params: emptyResult{}, result: workspaceListResult{}},
		"workspace.focus":  {handle: handleWorkspaceFocus, kinds: treeClients, params: workspaceIDParams{}, result: workspaceResult{}},
		"workspace.rename": {handle: handleWorkspaceRename, kinds: treeClients, params: workspaceRenameParams{}, result: workspaceResult{}},
		"workspace.close":  {handle: handleWorkspaceClose, kinds: treeClients, params: workspaceCloseParams{}, result: closedResult{}},

		"tab.create": {handle: handleTabCreate, kinds: treeClients, params: tabCreateParams{}, result: tabCreateResult{}},
		"tab.list":   {handle: handleTabList, kinds: treeClients, params: workspaceIDParams{}, result: tabListResult{}},
		"tab.focus":  {handle: handleTabFocus, kinds: treeClients, params: tabParams{}, result: tabResult{}},
		"tab.rename": {handle: handleTabRename, kinds: treeClients, params: tabRenameParams{}, result: tabResult{}},
		"tab.close":  {handle: handleTabClose, kinds: treeClients, params: tabParams{}, result: closedResult{}},

		"pane.split":  {handle: handlePaneSplit, kinds: treeClients, params: splitParams{}, result: paneSplitResult{}},
		"pane.list":   {handle: handlePaneList, kinds: treeClients, params: tabParams{}, result: paneListResult{}},
		"pane.get":    {handle: handlePaneGet, kinds: treeClients, params: paneParams{}, result: paneResult{}},
		"pane.focus":  {handle: handlePaneFocus, kinds: treeClients, params: paneParams{}, result: paneResult{}},
		"pane.rename": {handle: handlePaneRename, kinds: treeClients, params: paneRenameParams{}, result: paneResult{}},
		"pane.move":   {handle: handlePaneMove, kinds: interactiveClients, params: moveParams{}, result: paneMoveResult{}},
		"pane.close":  {handle: handlePaneClose, kinds: treeClients, params: paneParams{}, result: closedResult{}},

		// §2 lists `layouts` as a capability of its own, which is why these two are not
		// folded into the workspace surface above.
		"layout.export": {handle: handleLayoutExport, kinds: treeClients, params: layoutExportParams{}, result: Layout{}},
		"layout.apply":  {handle: handleLayoutApply, kinds: treeClients, params: applyParams{}, result: layoutApplyResult{}},
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

	// The handshake deadline (REQ-SEC-017) has done its job; an authenticated connection
	// may stay silent for as long as it likes.
	if err := c.netConn.SetReadDeadline(time.Time{}); err != nil {
		// Refused rather than INTERNAL_ERROR, so the connection closes now instead of
		// lingering unauthenticated until the deadline it could not clear.
		return nil, fmt.Errorf("%w: could not clear the handshake deadline: %w", ErrUnauthorized, err)
	}
	// From here on the connection's frame limit is the configured one, as it stands now
	// (API Spec §1): a later `limits.set` reaches new connections only.
	limit := c.server.maxMessageBytes.Load()
	c.limit.Store(limit)
	c.authenticated = true
	c.clientKind = kind
	c.connectionID = store.NewID(store.PrefixConnection)

	return helloResult{
		DaemonVersion:   c.server.cfg.DaemonVersion,
		ProtocolVersion: ProtocolVersion,
		Capabilities:    c.server.capabilities(),
		ConnectionID:    c.connectionID,
		MaxMessageBytes: limit,
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
	status.Frames = c.server.framesNow()
	return status, nil
}
