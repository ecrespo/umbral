package sdk

import (
	"context"
	"encoding/json"
	"errors"
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

// RequestPermission answers with the agent's own `allow_once` option for an allow and its
// `reject_once` option otherwise, and never with an `allow_always` one (REQ-ACP-003).
func (c *client) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	defer c.request()()
	allowed, err := c.p.h.cfg.Mediator.Permission(ctx, ports.PermissionRequest{ThreadID: c.p.thread, Call: c.known(req.ToolCall)})
	if err != nil || ctx.Err() != nil {
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
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

func (c *client) ReadTextFile(ctx context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	defer c.request()()
	content, err := c.p.h.cfg.Mediator.ReadFile(ctx, c.p.thread, req.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	return acp.ReadTextFileResponse{Content: content}, nil
}

func (c *client) WriteTextFile(ctx context.Context, req acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	defer c.request()()
	return acp.WriteTextFileResponse{}, c.p.h.cfg.Mediator.WriteFile(ctx, c.p.thread, req.Path, req.Content)
}

// errNoTerminals answers terminal requests until T-F2-08 runs them in Umbral sessions.
var errNoTerminals = errors.New("umbral does not run terminals for this agent yet")

func (c *client) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	defer c.request()()
	return acp.CreateTerminalResponse{}, errNoTerminals
}

func (c *client) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	defer c.request()()
	return acp.KillTerminalResponse{}, errNoTerminals
}

func (c *client) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	defer c.request()()
	return acp.TerminalOutputResponse{}, errNoTerminals
}

func (c *client) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	defer c.request()()
	return acp.ReleaseTerminalResponse{}, errNoTerminals
}

func (c *client) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	defer c.request()()
	return acp.WaitForTerminalExitResponse{}, errNoTerminals
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
