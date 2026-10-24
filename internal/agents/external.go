package agents

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
)

// runExternal runs a hosted agent's turn (REQ-ACP-002): the user's message goes to the agent
// as `session/prompt`, and what it reports is persisted and published as Umbral's own agent's
// would be, so the thread is read, waited on and attended like any other.
func (t *turnRun) runExternal(ctx context.Context) domain.StopReason {
	place, err := t.r.place(ctx, t.thread)
	if err != nil {
		return t.stopFor(ctx, storageErr(err))
	}
	t.cwd, t.writeRoot, t.mode = place.Cwd, place.WriteRoot, place.Mode
	sink := &externalSink{t: t, ctx: ctx, calls: map[string]domain.ToolCall{}}
	stop, err := t.r.cfg.Agents.Prompt(ctx, ports.ExternalRequest{ThreadID: t.thread.ID, Agent: t.thread.Agent, Dir: place.Cwd}, t.prompt, sink)
	if err != nil {
		return t.stopFor(ctx, err)
	}
	return stop
}

// externalSink persists a hosted agent's reports into the turn's assistant message and its
// tool calls, each before it is published (DD-007).
type externalSink struct {
	t   *turnRun
	ctx context.Context
	// messageID is the turn's assistant message, made at the first text or tool call.
	messageID string
	// calls are the turn's tool call rows, by the agent's id.
	calls map[string]domain.ToolCall
}

func (s *externalSink) ensureMessage(text string) error {
	if s.messageID != "" {
		return nil
	}
	cfg := s.t.r.cfg
	id := cfg.NewID(prefixMessage)
	if err := cfg.Store.AppendMessage(s.ctx, domain.Message{
		ID: id, ThreadID: s.t.thread.ID, TurnID: s.t.turnID, Role: domain.RoleAssistant, Content: text, CreatedAt: s.t.r.now(),
	}); err != nil {
		return storageErr(err)
	}
	s.messageID = id
	return nil
}

func (s *externalSink) Text(text string) error {
	if s.messageID == "" {
		if err := s.ensureMessage(text); err != nil {
			return err
		}
	} else if err := s.t.r.cfg.Store.AppendContent(s.ctx, s.messageID, text); err != nil {
		return storageErr(err)
	}
	s.t.r.publish(ports.ThreadDelta{ThreadID: s.t.thread.ID, TurnID: s.t.turnID, Kind: "text", Text: text})
	return nil
}

// Thought is shown, not kept, like a model's reasoning.
func (s *externalSink) Thought(text string) {
	s.t.r.publish(ports.ThreadDelta{ThreadID: s.t.thread.ID, TurnID: s.t.turnID, Kind: "reasoning", Text: text})
}

// ToolCall records a call the agent ran (REQ-ACP-007): `via = acp`, the risk its kind maps to,
// and `unmediated` until Umbral decides one of its requests (T-F2-08).
func (s *externalSink) ToolCall(ec ports.ExternalToolCall) error {
	if err := s.ensureMessage(""); err != nil {
		return err
	}
	c, ok := s.calls[ec.ID]
	if !ok {
		c = domain.ToolCall{
			ID: s.t.r.cfg.NewID(prefixToolCall), ThreadID: s.t.thread.ID, MessageID: s.messageID,
			Via: domain.ViaACP, Decision: domain.DecisionUnmediated, StartedAt: s.t.r.now(),
		}
	}
	c.Tool, c.Risk = toolName(ec), ec.Risk
	c.Args = toolArgs(ec)
	switch ec.Status {
	case "completed":
		c.Status = domain.ToolOK
	case "failed":
		c.Status = domain.ToolError
	default:
		c.Status = domain.ToolPending
	}
	if c.Status != domain.ToolPending {
		ended := s.t.r.now()
		c.EndedAt = &ended
		c.Result = ec.Output
		c.ResultSummary = firstLine(ec.Output)
	}
	if err := s.t.r.cfg.Store.SaveToolCall(s.ctx, c); err != nil {
		return storageErr(err)
	}
	s.calls[ec.ID] = c
	s.t.r.publish(ports.ThreadToolCall{Call: c})
	return nil
}

// toolName is the row's `tool`: `acp:` and the call's kind, a closed set, never the agent's
// free-text title, which would make the tool's name agent-chosen.
func toolName(ec ports.ExternalToolCall) string {
	kind := ec.Kind
	if kind == "" {
		kind = "other"
	}
	return "acp:" + kind
}

// toolArgs is the row's `args`: the agent's title and its own description of the call.
func toolArgs(ec ports.ExternalToolCall) json.RawMessage {
	args := map[string]any{"title": strings.TrimSpace(ec.Title)}
	if len(ec.RawInput) > 0 && json.Valid(ec.RawInput) {
		args["raw_input"] = json.RawMessage(ec.RawInput)
	}
	blob, err := json.Marshal(args)
	if err != nil {
		return json.RawMessage("{}")
	}
	return blob
}
