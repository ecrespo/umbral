package domain

import "errors"

// The module's sentinel errors. `internal/api` maps them to the JSON-RPC codes of API Spec
// §5.4; nothing below that layer knows a numeric code exists, which is the rule
// `internal/api/errors.go` enforces for every module.
var (
	// ErrNotFound reports an identifier that names nothing open — including a previous
	// pane identifier whose alias has gone with its terminal.
	ErrNotFound = errors.New("workspaces: not found")
	// ErrValidation reports parameters the contract rejects: an identifier outside the
	// REQ-WS-002 grammar, a direction or destination the API does not define, a label
	// past its cap.
	ErrValidation = errors.New("workspaces: invalid parameters")
	// ErrConflict reports a request that is well formed but cannot be honoured in the
	// current state — closing a workspace whose thread is still running (API Spec §5.4),
	// or moving the only pane of a tab into itself.
	ErrConflict = errors.New("workspaces: conflict")
)

// MaxLabelLength caps every label in the tree. The API sets no number, so one is set here
// rather than left to SQLite: a label is drawn in a tab bar, and an unbounded one is a way
// to make a client unreadable from across the socket.
const MaxLabelLength = 200
