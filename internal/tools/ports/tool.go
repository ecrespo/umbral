// Package ports is the ports layer of the tools module: the interface every tool implements,
// built-in or MCP, and the registry the agent runtime calls them through.
package ports

import (
	"context"
	"encoding/json"

	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// Tool is one tool the model can call.
type Tool interface {
	Spec() domain.Spec
	// Action is what the policy engine decides on (security.Decide): the risk and the target
	// its rules match — the path, the URL or the command line. Input has passed the schema.
	Action(env domain.Env, input json.RawMessage) (secdomain.Action, error)
	// Summary is one line for an approval prompt and for `tool_calls.result_summary`.
	Summary(input json.RawMessage) string
	// Run performs the call. The registry calls it only under a grant that allows it.
	Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error)
}

// Previewer is a tool that can show what it would change before it runs: the unified diff
// `approval.requested` carries for write_file and edit_file (REQ-AGT-012).
type Previewer interface {
	Preview(ctx context.Context, env domain.Env, input json.RawMessage) (string, error)
}

// Registry is the tools the agent runtime offers the model (REQ-AGT-002).
type Registry interface {
	// Specs lists every tool, ordered by name.
	Specs() []domain.Spec
	// Action validates the call's input and returns what to hand to security.Decide.
	Action(env domain.Env, call domain.Call) (secdomain.Action, error)
	// Preview returns the tool's diff, or "" for a tool that has none.
	Preview(ctx context.Context, env domain.Env, call domain.Call) (string, error)
	// Invoke runs the call if its grant allows it.
	Invoke(ctx context.Context, env domain.Env, call domain.Call, grant domain.Grant) (domain.Result, error)
}

// EgressLog records a request a tool sends off the machine (Art. 4, REQ-SEC-002): the same
// `egress_log` model calls are recorded in.
type EgressLog interface {
	Record(ctx context.Context, rec llmdomain.EgressRecord) error
}
