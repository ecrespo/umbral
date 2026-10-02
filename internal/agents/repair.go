package agents

import (
	"context"
	"errors"

	"github.com/ecrespo/umbral/internal/agents/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	toolsdomain "github.com/ecrespo/umbral/internal/tools/domain"
)

// errRepairFailed is an invalid tool call in the step that was the repair of another: the turn
// stops with tool_error (REQ-AGT-006).
var errRepairFailed = errors.New("the repaired tool call was invalid again")

// invalidCall records a call whose tool or arguments did not validate and counts it
// (REQ-OBS-002). The first one is answered with a repair message the model reads on its next
// step; one in that next step ends the turn (REQ-AGT-006).
func (t *turnRun) invalidCall(ctx context.Context, c domain.ToolCall, action secdomain.Action, cause error) error {
	// An unknown tool has no risk of its own; it is recorded as the most guarded one.
	c.Risk = string(secdomain.RiskExec)
	if action.Risk != "" {
		c.Risk = string(action.Risk)
	}
	t.r.cfg.Metrics.ToolCallInvalid(t.servedModel())
	t.invalid = true
	if t.repairing {
		if err := t.finishTool(ctx, c, domain.ToolInvalidArgs, "invalid call: "+cause.Error(), "", false); err != nil {
			return err
		}
		return errRepairFailed
	}
	return t.finishTool(ctx, c, domain.ToolInvalidArgs, repairMessage(c.Tool, cause), "", false)
}

// repairMessage is the one retry's instruction (REQ-AGT-006): what was wrong, and what a
// valid call looks like.
func repairMessage(tool string, cause error) string {
	if errors.Is(cause, toolsdomain.ErrUnknownTool) {
		return "invalid call: " + cause.Error() + ". There is no tool named " + tool +
			"; call one of the tools you were offered, by its exact name, with arguments that match its input schema."
	}
	return "invalid call: " + cause.Error() + ". Repair the arguments so they match the input schema of " + tool +
		" — a JSON object with every required field, of the right type — and call it again."
}

// servedModel is the model an invalid call is counted against: the one the router reported,
// else the one the thread names.
func (t *turnRun) servedModel() string {
	switch {
	case t.model != "":
		return t.model
	case t.thread.Model != "":
		return t.thread.Model
	default:
		return "unknown"
	}
}
