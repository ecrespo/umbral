package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	toolsdomain "github.com/ecrespo/umbral/internal/tools/domain"
)

// errStorage marks a write that failed: the turn stops with storage_error, and nothing that
// was not persisted is published or run (Analyze C-01, delta `2026-09-agent-runtime`).
var errStorage = errors.New("storage")

func storageErr(err error) error { return fmt.Errorf("%w: %w", errStorage, err) }

// unknown is the value a label or a state takes when the runtime does not know it: a turn's
// end that was not written, a model the router did not name, a tool the registry lacks.
const unknown = "unknown"

// turnRun is one turn's state.
type turnRun struct {
	r         *Runtime
	thread    domain.Thread
	turnID    string
	writeRoot string
	rules     []secdomain.Rule
	tools     []llm.ToolSpec
	tainted   bool
	usage     domain.Usage

	// model is the catalog id that served the last model call, as the router reports it.
	model string
	// notJSON are the calls of this step whose input was not JSON, by call id.
	notJSON map[string]bool
	// invalid is set when this step made an invalid call; repairing, when the step before
	// did, so this step is its one retry (REQ-AGT-006).
	invalid   bool
	repairing bool
	// ended is how the tool call running now ended, for its span.
	ended ports.ToolTrace
	// known are the registry's tools, the names a span may carry.
	known map[string]bool

	// system is the base prompt; summary is the one compaction left (delta
	// `2026-09-context-budget`, decision 5); history is what the model reads after them.
	system  string
	summary string
	history []llm.Message
}

// runTurn is the turn's loop: a model call, then the tools it asked for, until the model
// answers without asking for one or a limit stops it (REQ-AGT-008).
// It returns how the turn ended: its stop reason, and the moment its end was written, 0 when
// the write failed.
func (r *Runtime) runTurn(ctx context.Context, thread domain.Thread, turnID string) (domain.StopReason, int64) {
	// The turn's span holds everything below: model calls, tools and log lines (REQ-OBS-001).
	ctx, endSpan := r.cfg.Tracer.Turn(ctx, thread.ID, turnID)
	t := &turnRun{r: r, thread: thread, turnID: turnID}
	stop := t.run(ctx)

	// A cancelled turn — thread.cancel, or the daemon closing — leaves the thread stopped
	// (API §7); every other end leaves it idle.
	state := domain.StateIdle
	if stop == domain.StopCancelled {
		state = domain.StateStopped
	}
	// The end is written even when the turn was cancelled: that is what lets the thread take
	// the next message.
	// Retried once: a thread left `running` takes no message until the daemon restarts.
	ended := r.now()
	err := r.cfg.Store.FinishTurn(context.WithoutCancel(ctx), thread.ID, state, t.usage, ended)
	if err != nil {
		ended = r.now()
		err = r.cfg.Store.FinishTurn(context.WithoutCancel(ctx), thread.ID, state, t.usage, ended)
	}
	if err != nil {
		r.cfg.Logger.ErrorContext(ctx, "a turn's end could not be written", slog.String("thread", thread.ID),
			slog.String("turn", turnID), slog.Any("error", err))
		stop, ended = domain.StopStorageError, 0
	}
	endState := domain.AttentionAfter(state)
	switch {
	case ended == 0:
		// Nothing was written: the store still says running, so the end is not known.
		endState = unknown
	case state == domain.StateStopped:
		endState = string(domain.StateStopped)
	}
	endSpan(ports.TurnTrace{StopReason: stop, Model: t.model, Usage: t.usage})
	r.mu.Lock()
	r.ends.add(thread.ID, turnID, endState)
	r.mu.Unlock()
	r.publish(ports.TurnFinished{ThreadID: thread.ID, TurnID: turnID, StopReason: stop, Usage: t.usage, EndState: endState})
	return stop, ended
}

func (t *turnRun) run(ctx context.Context) domain.StopReason {
	if err := t.prepare(ctx); err != nil {
		return t.stopFor(ctx, err)
	}
	for step := 0; ; step++ {
		if step >= t.thread.MaxSteps {
			return domain.StopMaxSteps
		}
		if t.thread.TokensUsed+t.usage.InTokens+t.usage.OutTokens >= t.thread.BudgetTokens {
			return domain.StopBudget
		}
		if t.overCostCap() {
			return domain.StopBudget
		}
		calls, err := t.step(ctx)
		if err != nil {
			return t.stopFor(ctx, err)
		}
		if len(calls) == 0 {
			return domain.StopEndTurn
		}
		t.invalid = false
		for _, c := range calls {
			if err := t.runTool(ctx, c); err != nil {
				return t.stopFor(ctx, err)
			}
		}
		t.repairing = t.invalid
	}
}

// errBudget is a step that found the cost cap reached between its compaction and its call.
var errBudget = errors.New("agents: the thread's cost cap is reached")

// overCostCap reports whether the thread's spend — what earlier turns cost plus this one so far,
// compaction included — has reached the cap now in force (REQ-AGT-008, delta
// `2026-10-cost-cap`). It is checked before every priced call, so a thread overshoots the cap
// by one call at most: a stream cannot be priced until it ends.
func (t *turnRun) overCostCap() bool {
	limit := t.r.maxCost()
	return limit > 0 && t.thread.CostMicroUSD+t.usage.CostMicroUSD >= limit
}

// stopFor maps what ended a turn early to its stop reason.
func (t *turnRun) stopFor(ctx context.Context, err error) domain.StopReason {
	switch {
	case ctx.Err() != nil:
		return domain.StopCancelled
	case errors.Is(err, errStorage):
		t.r.cfg.Logger.ErrorContext(ctx, "a turn stopped: a write failed", slog.String("thread", t.thread.ID),
			slog.String("turn", t.turnID), slog.Any("error", err))
		return domain.StopStorageError
	case errors.Is(err, ctxdomain.ErrContextOverflow):
		return domain.StopContextOverflow
	case errors.Is(err, errBudget):
		return domain.StopBudget
	case errors.Is(err, errRepairFailed):
		t.r.cfg.Logger.WarnContext(ctx, "a turn stopped: a tool call was invalid after its repair", slog.String("thread", t.thread.ID),
			slog.String("turn", t.turnID), slog.String("model", t.servedModel()))
		return domain.StopToolError
	default:
		t.r.cfg.Logger.WarnContext(ctx, "a turn stopped: the model call failed", slog.String("thread", t.thread.ID),
			slog.String("turn", t.turnID), slog.Any("error", err))
		return domain.StopProviderError
	}
}

// prepare reads what the turn needs once: the history, the rules, the tools the mode
// exposes, and the system prompt with the rules files and git state.
func (t *turnRun) prepare(ctx context.Context) error {
	cfg := t.r.cfg
	t.writeRoot = secdomain.WriteRoot(t.thread.Cwd, cfg.IsRepo)
	rules, err := cfg.Store.Rules(ctx, t.thread.ID)
	if err != nil {
		return storageErr(err)
	}
	t.rules = rules
	t.known = map[string]bool{}
	for _, spec := range cfg.Tools.Specs() {
		t.known[spec.Name] = true
		// REQ-AGT-009: `ask` offers only ReadOnly tools; the policy refuses the rest anyway.
		if secdomain.Exposed(t.thread.Mode, spec.Risk) {
			t.tools = append(t.tools, llm.ToolSpec{Name: spec.Name, Description: spec.Description, InputSchema: spec.InputSchema})
		}
	}
	if err := t.loadHistory(ctx); err != nil {
		return storageErr(err)
	}
	in := ctxdomain.SystemInput{Cwd: t.thread.Cwd, Mode: string(t.thread.Mode)}
	if cfg.Context != nil {
		if in.Rules, err = cfg.Context.Rules(ctx, t.thread.Cwd); err != nil {
			return err
		}
		if in.Git, err = cfg.Context.Git(ctx, t.thread.Cwd); err != nil {
			return err
		}
	}
	t.system, err = ctxdomain.RenderSystem(in)
	return err
}

// loadHistory rebuilds what the model reads from the stored messages and tool calls: each
// assistant message with the calls it made and their results. A call a crash left pending is
// answered as interrupted, so the history the model reads stays well formed.
func (t *turnRun) loadHistory(ctx context.Context) error {
	msgs, err := t.r.cfg.Store.Messages(ctx, t.thread.ID)
	if err != nil {
		return err
	}
	calls, err := t.r.cfg.Store.ToolCalls(ctx, t.thread.ID)
	if err != nil {
		return err
	}
	byMessage := map[string][]domain.ToolCall{}
	for _, c := range calls {
		byMessage[c.MessageID] = append(byMessage[c.MessageID], c)
		t.tainted = t.tainted || c.Tainted
	}
	for _, m := range msgs {
		t.tainted = t.tainted || m.Tainted
		switch m.Role {
		case domain.RoleUser:
			t.history = append(t.history, llm.Message{Role: llm.RoleUser, Text: m.Content})
		case domain.RoleAssistant:
			am := llm.Message{Role: llm.RoleAssistant, Text: m.Content}
			for _, c := range byMessage[m.ID] {
				am.ToolCalls = append(am.ToolCalls, llm.ToolCall{ID: c.ID, Name: c.Tool, Input: string(c.Args)})
			}
			t.history = append(t.history, am)
			for _, c := range byMessage[m.ID] {
				t.history = append(t.history, toolMessage(c))
			}
		case domain.RoleSystemNote:
			// A compaction's record; each turn starts from the whole history and compacts
			// again if it must (delta `2026-09-agent-runtime`).
		}
	}
	return nil
}

func toolMessage(c domain.ToolCall) llm.Message {
	text := c.Result
	if c.Status == domain.ToolPending {
		text = "the call was interrupted before it finished"
	}
	return llm.Message{
		Role: llm.RoleTool, ToolCallID: c.ID, ToolName: c.Tool, Text: text,
		ToolError: c.Status != domain.ToolOK,
	}
}

// step makes one model call: compacts the context if it must, streams the answer — each
// chunk persisted, then published — and returns the tool calls the model asked for.
func (t *turnRun) step(ctx context.Context) ([]domain.ToolCall, error) {
	cfg := t.r.cfg
	req, err := t.compact(ctx, llm.Request{System: t.system, Messages: t.history, Tools: t.tools})
	if err != nil {
		return nil, err
	}
	// Compaction is a priced call of its own, so the cap is checked again before the main one.
	if t.overCostCap() {
		return nil, errBudget
	}
	stream, err := cfg.Models.Stream(ctx, ports.ModelCall{
		ThreadID: t.thread.ID, TurnID: t.turnID, Class: t.thread.ModelClass, Model: t.thread.Model, Request: req,
	})
	if err != nil {
		return nil, err
	}

	var answer strings.Builder
	var messageID string
	var asked []llm.ToolCall
	ensureMessage := func(text string) error {
		if messageID != "" {
			return nil
		}
		messageID = cfg.NewID(prefixMessage)
		err := cfg.Store.AppendMessage(ctx, domain.Message{
			ID: messageID, ThreadID: t.thread.ID, TurnID: t.turnID, Role: domain.RoleAssistant,
			Content: text, CreatedAt: t.r.now(),
		})
		if err != nil {
			return storageErr(err)
		}
		return nil
	}
	for ev, err := range stream {
		if err != nil {
			return nil, err
		}
		switch ev.Kind {
		case llm.EventTextDelta:
			if messageID == "" {
				if err := ensureMessage(ev.Text); err != nil {
					return nil, err
				}
			} else if err := cfg.Store.AppendContent(ctx, messageID, ev.Text); err != nil {
				return nil, storageErr(err)
			}
			answer.WriteString(ev.Text)
			t.r.publish(ports.ThreadDelta{ThreadID: t.thread.ID, TurnID: t.turnID, Kind: "text", Text: ev.Text})
		case llm.EventReasoningDelta:
			// Reasoning is shown, not kept: it is not part of the history a model reads back.
			t.r.publish(ports.ThreadDelta{ThreadID: t.thread.ID, TurnID: t.turnID, Kind: "reasoning", Text: ev.Text})
		case llm.EventToolCall:
			asked = append(asked, ev.ToolCall)
		case llm.EventUsage:
			t.usage.InTokens += ev.Usage.InputTokens
			t.usage.OutTokens += ev.Usage.OutputTokens
			t.usage.CostMicroUSD += ev.Usage.CostMicroUSD
			if ev.Usage.Model != "" {
				t.model = ev.Usage.Model
			}
		case llm.EventDone:
		}
	}
	if len(asked) > 0 {
		if err := ensureMessage(""); err != nil {
			return nil, err
		}
	}

	am := llm.Message{Role: llm.RoleAssistant, Text: answer.String()}
	calls := make([]domain.ToolCall, 0, len(asked))
	t.notJSON = map[string]bool{}
	for _, a := range asked {
		c := domain.ToolCall{
			ID: cfg.NewID(prefixToolCall), ThreadID: t.thread.ID, MessageID: messageID, Tool: a.Name,
			Args: json.RawMessage(a.Input), Status: domain.ToolPending, StartedAt: t.r.now(),
		}
		if len(bytes.TrimSpace(c.Args)) == 0 {
			// No arguments at all is the empty object, as the registry reads it.
			c.Args = json.RawMessage("{}")
		} else if !json.Valid(c.Args) {
			// The row holds JSON; the call is invalid and never reaches its tool as `{}`,
			// which a tool with no required field would accept.
			c.Args = json.RawMessage("{}")
			t.notJSON[c.ID] = true
		}
		calls = append(calls, c)
		am.ToolCalls = append(am.ToolCalls, llm.ToolCall{ID: c.ID, Name: a.Name, Input: a.Input})
	}
	if messageID != "" {
		t.history = append(t.history, am)
	}
	return calls, nil
}

// compact fits the request to the thread's budget (REQ-CTX-004). A compaction's summary is
// persisted as a system_note before context.compacted is published (DD-007).
func (t *turnRun) compact(ctx context.Context, req llm.Request) (llm.Request, error) {
	cfg := t.r.cfg
	budget := ctxdomain.NewBudget(cfg.Models.Window(ctx, t.thread.ModelClass, t.thread.Model), req.MaxOutputTokens)
	if fast := cfg.Models.Window(ctx, summaryClass, ""); fast > 0 {
		budget.SummaryInputTokens = ctxdomain.NewBudget(fast, summaryMaxTokens).Available() - ctxdomain.EstimateText(summaryPrompt)
	}
	out, compacted, err := ctxdomain.Compact(ctx, req, t.summary, budget, t.summarize)
	if err != nil || compacted == nil {
		return out, err
	}
	note := domain.Message{
		ID: cfg.NewID(prefixMessage), ThreadID: t.thread.ID, TurnID: t.turnID, Role: domain.RoleSystemNote,
		Content: compacted.Summary, CreatedAt: t.r.now(),
	}
	if err := cfg.Store.AppendMessage(ctx, note); err != nil {
		return llm.Request{}, storageErr(err)
	}
	t.summary, t.history = compacted.Summary, out.Messages
	t.r.publish(ports.ContextCompacted{ThreadID: t.thread.ID, BeforeTokens: compacted.BeforeTokens, AfterTokens: compacted.AfterTokens})
	return out, nil
}

// The summarizing call (delta `2026-09-context-budget`, decision 6).
const (
	summaryClass     = "fast"
	summaryMaxTokens = 1024
	summaryPrompt    = "Summarize the conversation below for the assistant that will continue it. " +
		"Keep the user's goals, decisions, file paths, commands and their outcomes, and what is still to do. " +
		"Write plain prose, no preamble."
)

// summarize is ctxdomain.Summarize over the `fast` class; what it consumes counts to the turn.
func (t *turnRun) summarize(ctx context.Context, transcript string) (string, error) {
	stream, err := t.r.cfg.Models.Stream(ctx, ports.ModelCall{
		ThreadID: t.thread.ID, TurnID: t.turnID, Class: summaryClass,
		Request: llm.Request{
			System: summaryPrompt, MaxOutputTokens: summaryMaxTokens,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: transcript}},
		},
	})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for ev, err := range stream {
		if err != nil {
			return "", err
		}
		switch ev.Kind {
		case llm.EventTextDelta:
			out.WriteString(ev.Text)
		case llm.EventUsage:
			t.usage.InTokens += ev.Usage.InputTokens
			t.usage.OutTokens += ev.Usage.OutputTokens
			t.usage.CostMicroUSD += ev.Usage.CostMicroUSD
		case llm.EventReasoningDelta, llm.EventToolCall, llm.EventDone:
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// runTool takes one tool call through the policy and, when a decision allows it, the tool.
// Every status change is persisted, then published; the model reads the result.
func (t *turnRun) runTool(ctx context.Context, c domain.ToolCall) error {
	// A tool the registry does not have is named by the model, so its span is not: that
	// would put model text in a span name and an unbounded label (delta `2026-10-otel`).
	name := c.Tool
	if !t.known[name] {
		name = unknown
	}
	ctx, endSpan := t.r.cfg.Tracer.Tool(ctx, c.ID, name)
	t.ended = ports.ToolTrace{}
	err := t.callTool(ctx, c)
	if t.ended.Status == "" {
		// The call stopped before a status was recorded: a failed write or a cancel.
		t.ended.Status = domain.ToolError
	}
	endSpan(t.ended)
	return err
}

// callTool runs one tool call: classify, record, decide, ask if it must, invoke.
func (t *turnRun) callTool(ctx context.Context, c domain.ToolCall) error {
	cfg := t.r.cfg
	env := toolsdomain.Env{ThreadID: t.thread.ID, Cwd: t.thread.Cwd, WriteRoot: t.writeRoot}
	call := toolsdomain.Call{Tool: c.Tool, Input: c.Args}

	var action secdomain.Action
	var err error
	if t.notJSON[c.ID] {
		err = fmt.Errorf("%w: %s: the input is not JSON", toolsdomain.ErrInvalidInput, c.Tool)
	} else {
		action, err = cfg.Tools.Action(env, call)
	}
	if errors.Is(err, toolsdomain.ErrUnknownTool) || errors.Is(err, toolsdomain.ErrInvalidInput) {
		return t.invalidCall(ctx, c, action, err)
	}
	if err != nil {
		// Not the model's fault — an environment the daemon built wrong: no repair, no count.
		c.Risk = string(secdomain.RiskExec)
		return t.finishTool(ctx, c, domain.ToolError, "error: "+err.Error(), "", false)
	}
	c.Risk = string(action.Risk)
	if err := cfg.Store.SaveToolCall(ctx, c); err != nil {
		return storageErr(err)
	}
	t.r.publish(ports.ThreadToolCall{Call: c})

	decision := secdomain.Decide(action, t.thread.Mode, t.rules, t.tainted)
	grant := toolsdomain.Grant{Action: action, Decision: decision}
	switch decision.Verdict {
	case secdomain.VerdictDeny:
		return t.finishTool(ctx, c, domain.ToolDeniedByPolicy, "denied by policy: "+decision.Reason, "", false)
	case secdomain.VerdictAsk:
		preview, _ := cfg.Tools.Preview(ctx, env, call)
		approved, err := t.awaitApproval(ctx, c, action, decision, preview)
		if err != nil {
			return err
		}
		if !approved {
			// REQ-AGT-005: the model reads the refusal and the turn goes on.
			return t.finishTool(ctx, c, domain.ToolDeniedByUser, "denied_by_user: the user did not allow this call", "", false)
		}
		grant.Approved = true
	case secdomain.VerdictAllow:
	}

	res, err := cfg.Tools.Invoke(ctx, env, call, grant)
	if err != nil {
		if ctx.Err() != nil {
			_ = t.finishTool(context.WithoutCancel(ctx), c, domain.ToolError, "cancelled", "", false)
			return ctx.Err()
		}
		return t.finishTool(ctx, c, domain.ToolError, "error: "+err.Error(), "", false)
	}
	return t.finishTool(ctx, c, domain.ToolOK, res.Text, res.Summary, res.Tainted)
}

// finishTool records a call's outcome, publishes it, and adds its result to the history.
func (t *turnRun) finishTool(ctx context.Context, c domain.ToolCall, status domain.ToolStatus, result, summary string, tainted bool) error {
	t.ended = ports.ToolTrace{Status: status, Risk: c.Risk}
	ended := t.r.now()
	c.Status, c.Result, c.ResultSummary, c.Tainted, c.EndedAt = status, result, summary, tainted, &ended
	if c.ResultSummary == "" {
		c.ResultSummary = firstLine(result)
	}
	if err := t.r.cfg.Store.SaveToolCall(ctx, c); err != nil {
		return storageErr(err)
	}
	t.r.publish(ports.ThreadToolCall{Call: c})
	t.tainted = t.tainted || tainted
	t.history = append(t.history, toolMessage(c))
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 200 {
		line = string([]rune(line)[:min(len([]rune(line)), 200)])
	}
	return line
}
