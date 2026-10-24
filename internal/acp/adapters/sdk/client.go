package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/acp-go-sdk"

	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

// maxToolOutput bounds the text kept of a tool call's reported content.
const maxToolOutput = 4096

// client is Umbral's side of one agent's connection: the agent's requests and its
// session updates, which go to the running turn's sink.
type client struct {
	p *process
	activity
	// outstanding counts the agent's requests of Umbral not answered yet; while any is, the
	// agent is waiting on Umbral and not silent (REQ-ACP-006).
	outstanding atomic.Int32

	mu    sync.Mutex
	sink  ports.Sink
	calls map[string]domain.ToolCall
	// failed is the first write the sink refused in this turn; failedCh closes with it.
	failed   error
	failedCh chan struct{}
}

var _ acp.Client = (*client)(nil)

// begin routes updates to sink until end.
func (c *client) begin(sink ports.Sink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink, c.calls, c.failed, c.failedCh = sink, map[string]domain.ToolCall{}, nil, make(chan struct{})
}

func (c *client) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink, c.calls = nil, nil
}

// fail records the sink's refusal; the turn ends with it.
func (c *client) fail(err error) {
	if c.failed == nil {
		c.failed = err
		close(c.failedCh)
	}
}

func (c *client) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed
}

func (c *client) waiting() bool { return c.outstanding.Load() > 0 }

// request brackets one of the agent's requests of Umbral.
func (c *client) request() func() {
	c.outstanding.Add(1)
	return func() {
		c.outstanding.Add(-1)
		c.touch()
	}
}

func (c *client) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sink == nil || c.failed != nil {
		// Between turns, or after a write failed: nothing to report to.
		return nil
	}
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil && t.Text != "" {
			if err := c.sink.Text(t.Text); err != nil {
				c.fail(err)
			}
		}
	case u.AgentThoughtChunk != nil:
		if t := u.AgentThoughtChunk.Content.Text; t != nil && t.Text != "" {
			c.sink.Thought(t.Text)
		}
	case u.ToolCall != nil:
		tc := u.ToolCall
		call := domain.ToolCall{
			ID: string(tc.ToolCallId), Title: tc.Title, Kind: string(tc.Kind), Status: string(tc.Status),
			RawInput: rawJSON(tc.RawInput), Output: contentText(tc.Content), Locations: locations(tc.Locations),
		}
		if call.Status == "" {
			call.Status = string(acp.ToolCallStatusPending)
		}
		c.report(call)
	case u.ToolCallUpdate != nil:
		tu := u.ToolCallUpdate
		call, ok := c.calls[string(tu.ToolCallId)]
		if !ok {
			call = domain.ToolCall{ID: string(tu.ToolCallId), Status: string(acp.ToolCallStatusPending)}
		}
		if tu.Title != nil {
			call.Title = *tu.Title
		}
		if tu.Kind != nil {
			call.Kind = string(*tu.Kind)
		}
		if tu.Status != nil {
			call.Status = string(*tu.Status)
		}
		if tu.RawInput != nil {
			call.RawInput = rawJSON(tu.RawInput)
		}
		if out := contentText(tu.Content); out != "" {
			call.Output = out
		}
		if tu.Locations != nil {
			call.Locations = locations(tu.Locations)
		}
		c.report(call)
	}
	// Plans, command lists and the rest are not mapped in F2 (REQ-ACP-002).
	return nil
}

// report hands the sink a tool call as it stands now. One that already ended stays ended: an
// agent's late update does not reopen it.
func (c *client) report(call domain.ToolCall) {
	if prev, ok := c.calls[call.ID]; ok && prev.Ended() {
		return
	}
	c.calls[call.ID] = call
	if err := c.sink.ToolCall(call); err != nil {
		c.fail(err)
	}
}

// known is the tool call the agent reported under id, if any, merged with what req says.
func (c *client) known(u acp.ToolCallUpdate) domain.ToolCall {
	c.mu.Lock()
	call, ok := c.calls[string(u.ToolCallId)]
	c.mu.Unlock()
	if !ok {
		call = domain.ToolCall{ID: string(u.ToolCallId)}
	}
	if u.Title != nil {
		call.Title = *u.Title
	}
	if u.Kind != nil {
		call.Kind = string(*u.Kind)
	}
	if u.RawInput != nil {
		call.RawInput = rawJSON(u.RawInput)
	}
	if u.Locations != nil {
		call.Locations = locations(u.Locations)
	}
	return call
}

// current is the running turn's sink, nil between turns.
func (c *client) current() ports.Sink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sink
}

// errNoTurn answers a request that arrives between turns: there is no turn to own its row or
// its approval (delta `2026-10-acp-mediation`, decision 1).
var errNoTurn = errors.New("umbral serves this agent's requests only while one of its turns runs")

// RequestPermission answers with the agent's own `allow_once` option for an allow and its
// `reject_once` option otherwise, and never with an `allow_always` one (REQ-ACP-003). The
// running turn decides; between turns, nothing is allowed.
func (c *client) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	defer c.request()()
	allowed := false
	if sink := c.current(); sink != nil {
		ok, err := sink.Permission(ctx, c.known(req.ToolCall))
		if err != nil || ctx.Err() != nil {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
		}
		allowed = ok
	}
	want := acp.PermissionOptionKindRejectOnce
	if allowed {
		want = acp.PermissionOptionKindAllowOnce
	}
	for _, o := range req.Options {
		if o.Kind == want {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId},
			}}, nil
		}
	}
	// No option of that kind: cancelling is the one answer that grants nothing.
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

// serve hands req to the running turn, which decides it and, allowed, runs perform.
func (c *client) serve(ctx context.Context, req domain.Request, perform func(context.Context, string) (string, error)) error {
	sink := c.current()
	if sink == nil {
		return errNoTurn
	}
	return sink.Request(ctx, req, perform)
}

func (c *client) ReadTextFile(ctx context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	defer c.request()()
	files := c.p.h.cfg.Files
	if files == nil {
		return acp.ReadTextFileResponse{}, errors.New("umbral does not serve file reads to this agent")
	}
	var content string
	err := c.serve(ctx, domain.Request{Method: domain.MethodReadFile, Path: req.Path}, func(_ context.Context, target string) (string, error) {
		var err error
		content, err = files.Read(target, deref(req.Line), deref(req.Limit))
		return fmt.Sprintf("read %d bytes", len(content)), err
	})
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	return acp.ReadTextFileResponse{Content: content}, nil
}

func (c *client) WriteTextFile(ctx context.Context, req acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	defer c.request()()
	files := c.p.h.cfg.Files
	if files == nil {
		return acp.WriteTextFileResponse{}, errors.New("umbral does not serve file writes to this agent")
	}
	return acp.WriteTextFileResponse{}, c.serve(ctx,
		domain.Request{Method: domain.MethodWriteFile, Path: req.Path, Content: req.Content},
		func(_ context.Context, target string) (string, error) {
			return fmt.Sprintf("wrote %d bytes", len(req.Content)), files.Write(target, req.Content)
		})
}

// CreateTerminal runs the command in a new session of the thread's once the turn allows it,
// within `[acp] max_terminals` (REQ-ACP-005).
func (c *client) CreateTerminal(ctx context.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	defer c.request()()
	p := c.p
	terms := p.h.cfg.Terminals
	if terms == nil {
		return acp.CreateTerminalResponse{}, terminalError("umbral does not run terminals for this agent")
	}
	command, policy, cwd, err := p.terminalCommand(req)
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	if err := p.reserveTerminal(); err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	var started ports.Terminal
	err = c.serve(ctx, domain.Request{
		Method: domain.MethodTerminal, Command: command, Policy: policy, Cwd: cwd,
		Script: domain.InterpretsScript(req.Command, req.Args),
	},
		func(ctx context.Context, target string) (string, error) {
			var err error
			started, err = terms.Start(ctx, p.thread, cwd, target)
			return "started", err
		})
	if err != nil {
		p.unreserve()
		return acp.CreateTerminalResponse{}, err
	}
	limit := defaultOutputLimit
	if req.OutputByteLimit != nil && *req.OutputByteLimit > 0 {
		limit = min(*req.OutputByteLimit, defaultOutputLimit)
	}
	id := p.addTerminal(&terminal{term: started, limit: limit})
	return acp.CreateTerminalResponse{TerminalId: id}, nil
}

func (c *client) TerminalOutput(_ context.Context, req acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	defer c.request()()
	t, err := c.p.terminal(req.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	out, exit := t.term.Output()
	out, truncated := tail(out, t.limit)
	resp := acp.TerminalOutputResponse{Output: out, Truncated: truncated}
	if exit != nil {
		resp.ExitStatus = &acp.TerminalExitStatus{ExitCode: exit.Code}
	}
	return resp, nil
}

// WaitForTerminalExit waits for the command to end; the agent is waiting on Umbral meanwhile,
// so its silence is not counted (REQ-ACP-006).
func (c *client) WaitForTerminalExit(ctx context.Context, req acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	defer c.request()()
	t, err := c.p.terminal(req.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	exit, err := t.term.Wait(ctx)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	return acp.WaitForTerminalExitResponse{ExitCode: exit.Code}, nil
}

func (c *client) KillTerminal(_ context.Context, req acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	defer c.request()()
	t, err := c.p.terminal(req.TerminalId)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	t.term.Kill()
	return acp.KillTerminalResponse{}, nil
}

func (c *client) ReleaseTerminal(ctx context.Context, req acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	defer c.request()()
	t := c.p.removeTerminal(req.TerminalId)
	if t == nil {
		return acp.ReleaseTerminalResponse{}, terminalError("no terminal %q", req.TerminalId)
	}
	return acp.ReleaseTerminalResponse{}, t.term.Release(ctx)
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func rawJSON(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func contentText(content []acp.ToolCallContent) string {
	var b strings.Builder
	for _, c := range content {
		switch {
		case c.Content != nil && c.Content.Content.Text != nil:
			b.WriteString(c.Content.Content.Text.Text)
		case c.Diff != nil:
			b.WriteString("diff of " + c.Diff.Path + "\n")
		}
		if b.Len() > maxToolOutput {
			break
		}
	}
	s := b.String()
	if len(s) > maxToolOutput {
		s = strings.ToValidUTF8(s[:maxToolOutput], "")
	}
	return s
}

func locations(ls []acp.ToolCallLocation) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Path)
	}
	return out
}
