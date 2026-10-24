package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	toolsdomain "github.com/ecrespo/umbral/internal/tools/domain"
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
	// The rules decide the agent's requests as they decide Umbral's own tools (REQ-ACP-003).
	if t.rules, err = t.r.cfg.Store.Rules(ctx, t.thread.ID); err != nil {
		return t.stopFor(ctx, storageErr(err))
	}
	sink := &externalSink{t: t, ctx: ctx, calls: map[string]domain.ToolCall{}, covered: map[string]bool{}}
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
	// mu guards what follows: the agent's reports and its requests arrive on goroutines of
	// their own.
	mu sync.Mutex
	// messageID is the turn's assistant message, made at the first text or tool call.
	messageID string
	// calls are the turn's tool call rows, by the agent's id.
	calls map[string]domain.ToolCall
	// covered are the paths allowed permissions of this turn listed, resolved: a write to one
	// is not decided again (REQ-ACP-004).
	covered map[string]bool
	// deciding serialises the decisions, so two requests never ask at once and the rules an
	// approval remembers are read before the next decision.
	deciding sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
// and `unmediated` unless Umbral decided its permission, which a later update keeps.
func (s *externalSink) ToolCall(ec ports.ExternalToolCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// reasonNoTarget is why a hosted call asks when its request names nothing the policy could
// decide on: an edit without locations, an execute without a command, a kind ACP lacks.
const reasonNoTarget = "no_target"

// The built-in tools' input keys a hosted agent's request is decided and recorded by.
const (
	argPath    = "path"
	argCommand = "command"
	argContent = "content"
)

// within merges the turn's context with the request's: a cancelled turn expires the request's
// approval, and a connection gone ends the wait too.
func (s *externalSink) within(ctx context.Context) (context.Context, context.CancelFunc) {
	merged, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	return merged, func() { stop(); cancel() }
}

func (s *externalSink) env(cwd string) toolsdomain.Env {
	if cwd == "" {
		cwd = s.t.cwd
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(s.t.cwd, cwd)
	}
	return toolsdomain.Env{ThreadID: s.t.thread.ID, Cwd: cwd, WriteRoot: s.t.writeRoot}
}

// action is the policy's view of one target of a hosted call, under the row's tool name, so a
// remembered decision matches the same kind and target again (delta `2026-10-acp-mediation`).
func (s *externalSink) action(tool, builtin string, env toolsdomain.Env, input map[string]string) (secdomain.Action, error) {
	if _, ok := input[argContent]; builtin == "write_file" && !ok {
		// write_file's schema requires its content; the target does not depend on it.
		input[argContent] = ""
	}
	blob, err := json.Marshal(input)
	if err != nil {
		return secdomain.Action{}, err
	}
	a, err := s.t.r.cfg.Tools.Action(env, toolsdomain.Call{Tool: builtin, Input: blob})
	if err != nil {
		return secdomain.Action{}, err
	}
	a.Tool = tool
	return a, nil
}

// decided is a call's verdict over its targets: the strictest one, with the action it was
// reached on.
type decided struct {
	action   secdomain.Action
	decision secdomain.Decision
}

func (s *externalSink) decide(actions []secdomain.Action, noTarget bool) decided {
	rank := map[secdomain.Verdict]int{secdomain.VerdictAllow: 0, secdomain.VerdictAsk: 1, secdomain.VerdictDeny: 2}
	var out decided
	for i, a := range actions {
		d := s.t.r.cfg.Policy().Decide(a, s.t.mode, s.t.rules, s.t.tainted)
		if i == 0 || rank[d.Verdict] > rank[out.decision.Verdict] {
			out = decided{action: a, decision: d}
		}
	}
	if noTarget && out.decision.Verdict == secdomain.VerdictAllow {
		out.decision = secdomain.Decision{Verdict: secdomain.VerdictAsk, Reason: reasonNoTarget}
	}
	return out
}

// permissionActions are the targets a permission is decided on (decision 2 of delta
// `2026-10-acp-mediation`); noTarget is a call that names none, which never allows unasked.
func (s *externalSink) permissionActions(tool string, ec ports.ExternalToolCall) (actions []secdomain.Action, covers []string, noTarget bool, err error) {
	env := s.env("")
	direct := func(risk secdomain.Risk, target string) secdomain.Action {
		return secdomain.Action{ThreadID: s.t.thread.ID, Tool: tool, Risk: risk, Target: target, Cwd: env.Cwd, WriteRoot: env.WriteRoot}
	}
	title := strings.TrimSpace(ec.Title)
	switch ec.Kind {
	case "edit", "delete", "move":
		for _, loc := range ec.Locations {
			a, err := s.action(tool, "write_file", env, map[string]string{argPath: loc})
			if err != nil {
				return nil, nil, false, err
			}
			actions, covers = append(actions, a), append(covers, a.Target)
		}
		if len(actions) == 0 {
			return []secdomain.Action{direct(secdomain.RiskWriteFS, title)}, nil, true, nil
		}
		return actions, covers, false, nil
	case "execute":
		if strings.TrimSpace(ec.Command) == "" {
			return []secdomain.Action{direct(secdomain.RiskExec, title)}, nil, true, nil
		}
		a, err := s.action(tool, "run_command", s.env(ec.Cwd), map[string]string{argCommand: ec.Command})
		return []secdomain.Action{a}, nil, false, err
	case "fetch":
		if ec.URL == "" {
			return []secdomain.Action{direct(secdomain.RiskNetwork, title)}, nil, true, nil
		}
		return []secdomain.Action{direct(secdomain.RiskNetwork, ec.URL)}, nil, false, nil
	case "read", "search", "think":
		target := title
		if len(ec.Locations) > 0 {
			target = ec.Locations[0]
		}
		return []secdomain.Action{direct(secdomain.RiskReadOnly, target)}, nil, false, nil
	default:
		return []secdomain.Action{direct(secdomain.RiskExec, title)}, nil, true, nil
	}
}

// Permission decides a hosted call's `session/request_permission` (REQ-ACP-003): the row the
// call reported gets the risk and the decision, an ask enters the approval queue pointing at
// it, and the locations of an allowed one cover this turn's writes to them (REQ-ACP-004).
func (s *externalSink) Permission(ctx context.Context, ec ports.ExternalToolCall) (bool, error) {
	ctx, cancel := s.within(ctx)
	defer cancel()
	s.deciding.Lock()
	defer s.deciding.Unlock()

	tool := toolName(ec)
	var d decided
	actions, covers, noTarget, err := s.permissionActions(tool, ec)
	if err != nil {
		// A target the tools could not resolve is not decided on: refused, never allowed, and
		// recorded so — the agent asked, so the row is not `unmediated` (REQ-ACP-007).
		s.t.r.cfg.Logger.WarnContext(ctx, "an ACP permission could not be decided", "thread", s.t.thread.ID, "error", err)
		d.decision = secdomain.Decision{Verdict: secdomain.VerdictDeny, Reason: reasonNoTarget}
		covers = nil
	} else {
		d = s.decide(actions, noTarget)
	}

	s.mu.Lock()
	if err := s.ensureMessage(""); err != nil {
		s.mu.Unlock()
		return false, err
	}
	c, ok := s.calls[ec.ID]
	if !ok {
		c = domain.ToolCall{
			ID: s.t.r.cfg.NewID(prefixToolCall), ThreadID: s.t.thread.ID, MessageID: s.messageID,
			Via: domain.ViaACP, Status: domain.ToolPending, StartedAt: s.t.r.now(),
			Tool: tool, Risk: ec.Risk, Args: toolArgs(ec),
		}
	}
	c.Decision = verdictDecision(d.decision.Verdict)
	err = s.t.r.cfg.Store.SaveToolCall(s.ctx, c) //nolint:contextcheck // the row is the turn's: written on its context, not the agent's request
	if err == nil {
		s.calls[ec.ID] = c
	}
	s.mu.Unlock()
	if err != nil {
		return false, storageErr(err)
	}
	s.t.r.publish(ports.ThreadToolCall{Call: c})

	allowed := d.decision.Verdict == secdomain.VerdictAllow
	if d.decision.Verdict == secdomain.VerdictAsk {
		if allowed, err = s.t.awaitApproval(ctx, c, d.action, d.decision, ""); err != nil {
			return false, err
		}
	}
	if allowed {
		s.mu.Lock()
		for _, p := range covers {
			s.covered[p] = true
		}
		s.mu.Unlock()
	}
	return allowed, nil
}

// Request decides one of the agent's requests of Umbral, records it as a row of its own and,
// allowed, performs it (REQ-ACP-004, REQ-ACP-005).
func (s *externalSink) Request(ctx context.Context, op ports.ExternalOp, perform func(context.Context, string) (string, error)) error {
	ctx, cancel := s.within(ctx)
	defer cancel()
	tool := "acp:" + op.Method

	s.deciding.Lock()
	a, d, args, err := s.requestDecision(ctx, tool, op)
	if err != nil {
		s.deciding.Unlock()
		return err
	}
	s.mu.Lock()
	if err := s.ensureMessage(""); err != nil {
		s.mu.Unlock()
		s.deciding.Unlock()
		return err
	}
	c := domain.ToolCall{
		ID: s.t.r.cfg.NewID(prefixToolCall), ThreadID: s.t.thread.ID, MessageID: s.messageID, Tool: tool,
		Risk: string(a.Risk), Args: args, Via: domain.ViaACP, Decision: verdictDecision(d.Verdict),
		Status: domain.ToolPending, StartedAt: s.t.r.now(),
	}
	err = s.t.r.cfg.Store.SaveToolCall(s.ctx, c) //nolint:contextcheck // the row is the turn's: written on its context, not the agent's request
	s.mu.Unlock()
	if err != nil {
		s.deciding.Unlock()
		return storageErr(err)
	}
	s.t.r.publish(ports.ThreadToolCall{Call: c})

	allowed := d.Verdict == secdomain.VerdictAllow
	status := domain.ToolDeniedByPolicy
	if d.Verdict == secdomain.VerdictAsk {
		diff := ""
		if op.Method == ports.OpWriteFile {
			diff, _ = s.t.r.cfg.Tools.Preview(ctx, s.env(""), toolsdomain.Call{Tool: "write_file", Input: mustJSON(map[string]string{argPath: op.Path, "content": op.Content})})
		}
		allowed, err = s.t.awaitApproval(ctx, c, a, d, diff)
		if err != nil {
			s.deciding.Unlock()
			_ = s.finish(ctx, c, domain.ToolError, "cancelled")
			return err
		}
		status = domain.ToolDeniedByUser
	}
	s.deciding.Unlock()
	if !allowed {
		if err := s.finish(ctx, c, status, "denied: "+d.Reason); err != nil {
			return err
		}
		return ports.ErrDenied
	}
	summary, perr := perform(ctx, a.Target)
	if perr != nil {
		if err := s.finish(ctx, c, domain.ToolError, "error: "+perr.Error()); err != nil {
			return err
		}
		return perr
	}
	return s.finish(ctx, c, domain.ToolOK, summary)
}

// requestDecision is what a request is decided on, and the verdict (decision 6 and 7 of delta
// `2026-10-acp-mediation`).
func (s *externalSink) requestDecision(ctx context.Context, tool string, op ports.ExternalOp) (secdomain.Action, secdomain.Decision, json.RawMessage, error) {
	switch op.Method {
	case ports.OpReadFile, ports.OpWriteFile:
		if !filepath.IsAbs(op.Path) {
			return secdomain.Action{}, secdomain.Decision{}, nil, fmt.Errorf("%w: %s needs an absolute path, not %q", ports.ErrDenied, op.Method, op.Path)
		}
		// The write's resolution is the read's too: links followed, against the write root.
		a, err := s.action(tool, "write_file", s.env(""), map[string]string{argPath: op.Path})
		if err != nil {
			return secdomain.Action{}, secdomain.Decision{}, nil, fmt.Errorf("%w: %w", ports.ErrDenied, err)
		}
		if op.Method == ports.OpReadFile {
			a.Risk = secdomain.RiskReadOnly
			d := s.t.r.cfg.Policy().Decide(a, s.t.mode, s.t.rules, s.t.tainted)
			if d.Verdict == secdomain.VerdictAllow && !secdomain.WithinWriteRoot(a) {
				d = secdomain.Decision{Verdict: secdomain.VerdictAsk, Reason: secdomain.ReasonOutsideWriteRoot}
			}
			return a, d, mustJSON(map[string]string{argPath: a.Target}), nil
		}
		s.mu.Lock()
		covered := s.covered[a.Target]
		s.mu.Unlock()
		args := mustJSON(map[string]any{argPath: a.Target, "bytes": len(op.Content)})
		if covered {
			return a, secdomain.Decision{Verdict: secdomain.VerdictAllow}, args, nil
		}
		return a, s.t.r.cfg.Policy().Decide(a, s.t.mode, s.t.rules, s.t.tainted), args, nil
	case ports.OpTerminal:
		a, err := s.action(tool, "run_command", s.env(op.Cwd), map[string]string{argCommand: op.Command})
		if err != nil {
			return secdomain.Action{}, secdomain.Decision{}, nil, fmt.Errorf("%w: %w", ports.ErrDenied, err)
		}
		d := s.t.r.cfg.Policy().Decide(a, s.t.mode, s.t.rules, s.t.tainted)
		if op.Policy != "" && op.Policy != op.Command {
			// The line as the normaliser must read it, one token per word the shell sees: the
			// strictest verdict stands, and the approval shows the line as typed.
			pa, err := s.action(tool, "run_command", s.env(op.Cwd), map[string]string{argCommand: op.Policy})
			if err != nil {
				return secdomain.Action{}, secdomain.Decision{}, nil, fmt.Errorf("%w: %w", ports.ErrDenied, err)
			}
			d = s.decide([]secdomain.Action{a, pa}, false).decision
		}
		if d.Verdict == secdomain.VerdictAllow && !secdomain.WithinWriteRoot(secdomain.Action{Target: a.Cwd, WriteRoot: a.WriteRoot}) {
			// REQ-ACP-005: a terminal outside the write root asks, whatever allowed the line.
			d = secdomain.Decision{Verdict: secdomain.VerdictAsk, Reason: secdomain.ReasonOutsideWriteRoot}
		}
		return a, d, mustJSON(map[string]string{argCommand: op.Command, "cwd": a.Cwd}), nil
	default:
		return secdomain.Action{}, secdomain.Decision{}, nil, fmt.Errorf("%w: %q is not a request Umbral serves", ports.ErrDenied, op.Method)
	}
}

// finish records how a request ended. It is written even when the agent's request was cancelled
// meanwhile: the row exists, and what happened to it is the turn's record.
func (s *externalSink) finish(ctx context.Context, c domain.ToolCall, status domain.ToolStatus, result string) error {
	ended := s.t.r.now()
	c.Status, c.Result, c.ResultSummary, c.EndedAt = status, result, firstLine(result), &ended
	if err := s.t.r.cfg.Store.SaveToolCall(context.WithoutCancel(ctx), c); err != nil {
		return storageErr(err)
	}
	s.t.r.publish(ports.ThreadToolCall{Call: c})
	return nil
}

func mustJSON(v any) json.RawMessage {
	blob, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return blob
}
