package agents

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ecrespo/umbral/internal/agents/domain"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
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

// unreadableRepair is the repair message of a step whose tool call its provider could not read
// (REQ-AGT-006): there is no call to answer, so it is a user message added to the retry.
const unreadableRepair = "Your last tool call could not be read as JSON. Call the tool again with its " +
	"arguments as one JSON object that matches its input schema, with no text before or after it " +
	"and no escapes but JSON's own (\\n, \\t, \\\" and \\uXXXX)."

// unreadableCall handles a step whose tool call its provider could not read: the call failed
// before its first event — every candidate tried, one of them that way — or in the stream after
// the reasoning it showed. It is the model's invalid call (REQ-AGT-006, REQ-OBS-002): counted,
// and the step is asked again once with unreadableRepair added to the request only, never
// stored. In a step that was itself a retry it is counted but not handled, and the turn stops
// with tool_error.
func (t *turnRun) unreadableCall(ctx context.Context, err error) bool {
	if ctx.Err() != nil || !errors.Is(err, llm.ErrMalformedToolCall) {
		return false
	}
	t.r.cfg.Metrics.ToolCallInvalid(t.servedModel())
	if t.repairing {
		return false
	}
	// The error is not logged: Ollama's carries the model's own output.
	t.r.cfg.Logger.InfoContext(ctx, "the provider could not read the model's tool call; the step is asked again",
		slog.String("thread", t.thread.ID), slog.String("turn", t.turnID), slog.String("model", t.servedModel()))
	t.history = append(t.history, llm.Message{Role: llm.RoleUser, Text: unreadableRepair})
	t.repairing = true
	return true
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
		return unknown
	}
}
