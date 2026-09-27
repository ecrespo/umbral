// Package api exposes the JSON-RPC 2.0 contract of specs/api/umbral-daemon-api-v1.md
// over a 0600 Unix socket, and translates domain errors into protocol codes
// (Tech Design §5.4).
//
// It is the only package that knows about the wire format. Modules hand it domain types
// and sentinel errors; how those become codes and NDJSON lines stays here.
package api

import "encoding/json"

// jsonrpcVersion is the only value the "jsonrpc" field may carry (API Spec §1).
const jsonrpcVersion = "2.0"

// ProtocolVersion is the handshake version this daemon speaks (API Spec §2).
// Within a major version the contract stays compatible (Art. 8).
const ProtocolVersion = 1

// MaxMessageBytes is the framing limit of API Spec §1 **before the handshake**, in both
// directions. It is fixed: the configured limit (`[api] max_message_bytes`, up to 64 MiB)
// applies only once `system.hello` succeeds, so an unauthenticated peer cannot make the
// daemon reserve more than this per connection. A longer line is rejected rather than
// buffered: an unbounded reader on a local socket is a denial-of-service waiting to happen.
const MaxMessageBytes = 4 << 20 // 4 MiB

// request is an incoming JSON-RPC message. A request without an id is a notification
// and gets no reply.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the client expects no reply.
func (r request) isNotification() bool { return len(r.ID) == 0 }

// hasID reports whether the request carries an id a reply can be matched against: present
// and not null. JSON-RPC allows a null id but discourages it, and the daemon itself uses
// null only for "undetermined" (REQ-SEC-018).
func (r request) hasID() bool { return len(r.ID) != 0 && string(r.ID) != "null" }

// response is an outgoing reply. Exactly one of Result and Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`

	// method is what the response answers. It is never sent; it names the method in the
	// log line when the response is over the frame limit (REQ-OBS-005).
	method string
}

// notification is a daemon-to-client message (API Spec §6). It carries no id.
//
// `seq` is the envelope counter of §1: one daemon run, shared by every connection, so the
// same event carries the same number for everyone. It is not `session.output`'s `seq`, which
// lives in the parameters and counts per PTY session (§5.11) — two different numbers with
// the same name in different places, which is why each says so where it is defined.
//
// It is never omitted, not even at 0. An absent member and a zero would be the same on the
// wire, and a client cannot tell "the first notification of this run" from "a daemon that
// does not sequence" if the field can vanish.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Seq     uint64 `json:"seq"`
	Params  any    `json:"params,omitempty"`
}

// wireError is the error object of API Spec §3.
type wireError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *errorData `json:"data,omitempty"`
}

// errorData carries the domain code, the per-field details and the trace id that every
// INTERNAL_ERROR must include (Art. 7).
type errorData struct {
	DomainCode string       `json:"domain_code"`
	Details    []ErrorField `json:"details,omitempty"`
	TraceID    string       `json:"trace_id,omitempty"`
	// Supported lists the protocol versions this daemon accepts. It is only set on
	// UNSUPPORTED_PROTOCOL_VERSION, where API Spec §2 requires it.
	Supported []int `json:"supported,omitempty"`
	// SizeBytes and LimitBytes are set only on RESULT_TOO_LARGE: the size the result would
	// have had and this connection's limit, as numbers a client can act on (REQ-API-005).
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	LimitBytes *int64 `json:"limit_bytes,omitempty"`
}

// ErrorField explains which field of the request was wrong.
type ErrorField struct {
	Field string `json:"field"`
	Issue string `json:"issue"`
}

// helloParams is the handshake request (API Spec §2).
type helloParams struct {
	Token           string `json:"token"`
	ClientKind      string `json:"client_kind"`
	ClientVersion   string `json:"client_version"`
	ProtocolVersion int    `json:"protocol_version"`
}

// helloResult is the handshake reply.
type helloResult struct {
	DaemonVersion   string   `json:"daemon_version"`
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
	ConnectionID    string   `json:"connection_id"`
	// MaxMessageBytes is this connection's frame limit from here on, in both directions
	// (API Spec §5.1). A client reads with it; one that ignores it keeps reading at 4 MiB.
	MaxMessageBytes int64 `json:"max_message_bytes"`
}

// StatusResult is the payload of system.status (API Spec §5.2), used by `umb status`.
type StatusResult struct {
	DaemonVersion  string           `json:"daemon_version"`
	UptimeMillis   int64            `json:"uptime_ms"`
	SessionsAlive  int              `json:"sessions_alive"`
	ThreadsRunning int              `json:"threads_running"`
	Providers      []ProviderStatus `json:"providers"`
	MCP            []MCPStatus      `json:"mcp"`
	// Frames is how close traffic has come to the frame limit this run (REQ-OBS-005). The
	// daemon fills it; a StatusFunc leaves it alone.
	Frames Frames `json:"frames"`
}

// ProviderStatus is one model provider's health in system.status.
type ProviderStatus struct {
	ID     string `json:"id"`
	Health string `json:"health"`
	// Reason says why Health is down or degraded — `keyring_unavailable`, `env_secret` —
	// and is absent otherwise (REQ-SEC-008, REQ-SEC-012).
	Reason string `json:"reason,omitempty"`
}

// MCPStatus is one MCP server's state in system.status.
type MCPStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
}
