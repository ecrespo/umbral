// Package domain is the domain layer of the tools module, which owns the tool registry and
// the built-in file, search and network tools: what a tool declares, what a call carries and
// what it returns. It imports nothing outside the standard library and other modules' domain
// (Art. 3).
package domain

import (
	"encoding/json"
	"errors"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// Spec is what a tool declares to the model and to the policy engine (REQ-AGT-002): a name, a
// description, a JSON Schema for its input and a risk class.
type Spec struct {
	Name        string
	Description string
	Risk        secdomain.Risk
	// InputSchema is a JSON Schema object (draft 2020-12).
	InputSchema map[string]any
}

// Env is where a call runs: the thread's working directory and write root. Relative paths
// resolve against Cwd.
type Env struct {
	ThreadID  string
	Cwd       string
	WriteRoot string
	// Target is set by the registry to the target the grant was checked against, with its
	// links resolved at that check. A writer writes there and resolves nothing again, so a
	// link swapped in since fails the write instead of redirecting it.
	Target string
}

// Result is what a tool returns to the model. Summary is the one line `tool_calls.
// result_summary` keeps; Text is what the model reads. Tainted marks content from outside the
// user's machine and control (REQ-SEC-006): the turn that reads it asks before Exec and
// Network tools.
type Result struct {
	Summary string
	Text    string
	Tainted bool
}

// Grant is the policy decision a call runs under, and the action it was decided on. The
// registry recomputes the call's action and refuses the call unless it is that one and the
// decision allows it: a grant cannot be carried to another call, nor survive a target that
// moved — a symlink swapped while the user read the approval (AGENTS.md: no tool outside
// security.Decide()). Approved is the user's answer to an `ask`.
type Grant struct {
	Action   secdomain.Action
	Decision secdomain.Decision
	Approved bool
}

// Allows reports whether the grant lets the call run: the policy allowed it, or it asked and
// the user approved.
func (g Grant) Allows() bool {
	switch g.Decision.Verdict {
	case secdomain.VerdictAllow:
		return true
	case secdomain.VerdictAsk:
		return g.Approved
	default:
		return false
	}
}

// Call is one tool call the model made.
type Call struct {
	Tool  string
	Input json.RawMessage
}

var (
	// ErrUnknownTool is a call to a tool the registry does not have.
	ErrUnknownTool = errors.New("unknown tool")
	// ErrInvalidInput is input its tool's schema refuses (`tool_calls.status = invalid_args`).
	ErrInvalidInput = errors.New("invalid tool input")
	// ErrNotAuthorized is a call whose grant does not allow it, or was decided on another
	// action.
	ErrNotAuthorized = errors.New("the policy does not allow this call")
	// ErrInvalidEnv is a call whose working directory or write root is not an absolute path:
	// a relative one would resolve against the daemon's own directory, which nobody approved.
	ErrInvalidEnv = errors.New("the call's working directory and write root must be absolute")
)
