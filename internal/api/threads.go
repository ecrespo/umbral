package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"

	waitsports "github.com/ecrespo/umbral/internal/waits/ports"
)

// Thread is API Spec §4's Thread.
type Thread struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Mode           string  `json:"mode"`
	Model          *string `json:"model"`
	ModelClass     string  `json:"model_class"`
	CWD            string  `json:"cwd"`
	State          string  `json:"state"`
	AttentionState string  `json:"attention_state"`
	Ephemeral      bool    `json:"ephemeral"`
	MaxSteps       int     `json:"max_steps"`
	BudgetTokens   int64   `json:"budget_tokens"`
	TokensUsed     int64   `json:"tokens_used"`
	CostMicroUSD   int64   `json:"cost_micro_usd"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

func toWireThread(t agentsdomain.Thread) Thread {
	return Thread{
		ID: t.ID, Title: t.Title, Mode: string(t.Mode), Model: emptyAsNull(t.Model), ModelClass: t.ModelClass,
		CWD: t.Cwd, State: string(t.State), AttentionState: t.AttentionState, Ephemeral: t.Ephemeral,
		MaxSteps: t.MaxSteps, BudgetTokens: t.BudgetTokens, TokensUsed: t.TokensUsed, CostMicroUSD: t.CostMicroUSD,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// MessageAttachment is `attachments[]` of a Message.
type MessageAttachment struct {
	Kind           string `json:"kind"`
	Ref            string `json:"ref"`
	Bytes          int64  `json:"bytes"`
	TruncatedBytes int64  `json:"truncated_bytes"`
}

// Message is API Spec §4's Message.
type Message struct {
	ID          string              `json:"id"`
	ThreadID    string              `json:"thread_id"`
	TurnID      string              `json:"turn_id"`
	Role        string              `json:"role"`
	Content     string              `json:"content"`
	Attachments []MessageAttachment `json:"attachments"`
	CreatedAt   int64               `json:"created_at"`
}

func toWireMessage(m agentsdomain.Message) Message {
	out := Message{
		ID: m.ID, ThreadID: m.ThreadID, TurnID: m.TurnID, Role: string(m.Role), Content: m.Content,
		Attachments: []MessageAttachment{}, CreatedAt: m.CreatedAt,
	}
	for _, a := range m.Attachments {
		out.Attachments = append(out.Attachments, MessageAttachment(a))
	}
	return out
}

// ToolCall is API Spec §4's ToolCall.
type ToolCall struct {
	ID            string          `json:"id"`
	ThreadID      string          `json:"thread_id"`
	MessageID     string          `json:"message_id"`
	Tool          string          `json:"tool"`
	Risk          string          `json:"risk"`
	Args          json.RawMessage `json:"args"`
	Status        string          `json:"status"`
	ResultSummary *string         `json:"result_summary"`
	BlockID       *string         `json:"block_id"`
	StartedAt     int64           `json:"started_at"`
	EndedAt       *int64          `json:"ended_at"`
}

func toWireToolCall(c agentsdomain.ToolCall) ToolCall {
	args := c.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return ToolCall{
		ID: c.ID, ThreadID: c.ThreadID, MessageID: c.MessageID, Tool: c.Tool, Risk: c.Risk, Args: args,
		Status: string(c.Status), ResultSummary: emptyAsNull(c.ResultSummary), BlockID: emptyAsNull(c.BlockID),
		StartedAt: c.StartedAt, EndedAt: c.EndedAt,
	}
}

// The payloads of §6's thread notifications.
type threadDeltaPayload struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

type turnUsage struct {
	InTokens     int64 `json:"in_tokens"`
	OutTokens    int64 `json:"out_tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

type turnFinishedPayload struct {
	ThreadID   string    `json:"thread_id"`
	TurnID     string    `json:"turn_id"`
	StopReason string    `json:"stop_reason"`
	Usage      turnUsage `json:"usage"`
}

type contextCompactedPayload struct {
	ThreadID     string `json:"thread_id"`
	BeforeTokens int64  `json:"before_tokens"`
	AfterTokens  int64  `json:"after_tokens"`
}

// fieldThreadID is the parameter every thread method but create names.
const fieldThreadID = "thread_id"

// hasThreads is `threads`'s availability.
func hasThreads(cfg Config) bool { return cfg.Threads != nil }

func threads(c *conn) (agentsports.Threads, error) {
	if c.server.cfg.Threads == nil {
		return nil, fmt.Errorf("%w: the agent runtime is not wired in", ErrNotImplemented)
	}
	return c.server.cfg.Threads, nil
}

// threadMethods is §5.19–§5.23. §2's `cli` row gives `umb` thread.create, thread.send and
// thread.cancel — what `umb ai` needs, and a way to stop it — and not get, list or update:
// `umb` must not, for one, switch a thread to `auto-edit`.
func threadMethods() map[string]method {
	return map[string]method{
		"thread.create": {handle: handleThreadCreate, params: createThreadParams{}, result: Thread{}},
		"thread.send":   {handle: handleThreadSend, params: sendParams{}, result: sendResult{}},
		"thread.cancel": {handle: handleThreadCancel, params: cancelThreadParams{}, result: cancelResult{}},
		"thread.get":    {handle: handleThreadGet, kinds: interactiveClients, params: getThreadParams{}, result: threadResult{}},
		"thread.list":   {handle: handleThreadList, kinds: interactiveClients, params: emptyResult{}, result: threadListResult{}},
		"thread.update": {handle: handleThreadUpdate, kinds: interactiveClients, params: updateThreadParams{}, result: Thread{}},
	}
}

type createThreadParams struct {
	Mode         string `json:"mode" api:"optional"`
	Model        string `json:"model" api:"optional"`
	ModelClass   string `json:"model_class" api:"optional"`
	CWD          string `json:"cwd"`
	Title        string `json:"title" api:"optional"`
	Ephemeral    bool   `json:"ephemeral" api:"optional"`
	MaxSteps     int    `json:"max_steps" api:"optional"`
	BudgetTokens int64  `json:"budget_tokens" api:"optional"`
}

func handleThreadCreate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p createThreadParams
	if err := decode(raw, &p, "thread.create"); err != nil {
		return nil, err
	}
	if p.CWD == "" {
		return nil, ValidationError("cwd is required", ErrorField{Field: "cwd", Issue: requiredTag})
	}
	t, err := svc.Create(ctx, agentsdomain.CreateParams{
		Mode: secdomain.Mode(p.Mode), Model: p.Model, ModelClass: p.ModelClass, Cwd: p.CWD, Title: p.Title,
		Ephemeral: p.Ephemeral, MaxSteps: p.MaxSteps, BudgetTokens: p.BudgetTokens,
	})
	if err != nil {
		return nil, err
	}
	return toWireThread(t), nil
}

// sendAttachment is one of thread.send's attachments: a `ref`, or for `stdin` the data
// inline (API §5.20). DataB64 is a pointer because an empty stdin is still data, and only
// an absent field is no data.
type sendAttachment struct {
	Kind      string  `json:"kind"`
	Ref       string  `json:"ref" api:"optional"`
	DataB64   *string `json:"data_b64" api:"optional"`
	Truncated bool    `json:"truncated" api:"optional"`
}

// kindStdin is the one attachment kind that carries its data (REQ-CLI-001).
const kindStdin = "stdin"

type sendParams struct {
	ThreadID    string           `json:"thread_id"`
	Text        string           `json:"text"`
	Attachments []sendAttachment `json:"attachments" api:"optional"`
	ClientMsgID string           `json:"client_msg_id" api:"optional"`
	Wait        json.RawMessage  `json:"wait" api:"optional"`
}

// sendWait is thread.send's optional `wait` (API §5.20).
type sendWait struct {
	Until     []string `json:"until"`
	TimeoutMS int64    `json:"timeout_ms"`
}

type sendResult struct {
	TurnID    string `json:"turn_id"`
	MessageID string `json:"message_id"`
	// FinalState is present only when the send brought a wait (API §5.20).
	FinalState *string `json:"final_state,omitempty" api:"optional"`
}

func handleThreadSend(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p sendParams
	if err := decode(raw, &p, "thread.send"); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, ValidationError("thread_id is required", ErrorField{Field: fieldThreadID, Issue: requiredTag})
	}
	// A wait is validated and subscribed before anything is sent, so an invalid one sends
	// nothing and a valid one misses none of the turn's events (REQ-AUT-001).
	var wait waitsports.ThreadWait
	if len(p.Wait) > 0 && string(p.Wait) != jsonNull {
		if c.server.cfg.Waits == nil {
			return nil, fmt.Errorf("%w: thread.send's wait: the wait engine is not wired in", ErrNotImplemented)
		}
		var w sendWait
		if err := decode(p.Wait, &w, "thread.send's wait"); err != nil {
			return nil, err
		}
		if wait, err = c.server.cfg.Waits.Thread(ctx, p.ThreadID, w.Until, w.TimeoutMS); err != nil {
			return nil, err
		}
	}
	refs, err := sendAttachments(p.Attachments)
	if err != nil {
		if wait != nil {
			wait.Close()
		}
		return nil, err
	}
	res, err := svc.Send(ctx, agentsports.SendParams{
		ThreadID: p.ThreadID, Text: p.Text, Attachments: refs, ClientMsgID: p.ClientMsgID, RejectBlocked: wait != nil,
	})
	if err != nil {
		if wait != nil {
			wait.Close()
		}
		return nil, err
	}
	if wait == nil {
		return sendResult{TurnID: res.TurnID, MessageID: res.MessageID}, nil
	}
	// One ordered submission (DD-011): the wait is pinned to the turn this send started — or
	// the one its repeated client_msg_id names — before the next request is read.
	wait.Pin(res.TurnID)
	return deferred(func(ctx context.Context) (any, error) {
		got, err := wait.Wait(ctx)
		if err != nil {
			return nil, err
		}
		state := string(got.State)
		return sendResult{TurnID: res.TurnID, MessageID: res.MessageID, FinalState: &state}, nil
	}), nil
}

// sendAttachments checks thread.send's attachments and decodes a `stdin` one's data. A
// `stdin` attachment carries `data_b64` and nothing else, at most MaxStdinBytes of it once
// decoded, and comes once per message; every other kind carries a `ref` and nothing else.
func sendAttachments(in []sendAttachment) ([]agentsports.AttachmentRef, error) {
	refs := make([]agentsports.AttachmentRef, 0, len(in))
	stdin := false
	for i, a := range in {
		field := fmt.Sprintf("attachments[%d]", i)
		if a.Kind != kindStdin {
			if a.DataB64 != nil || a.Truncated || a.Ref == "" {
				return nil, ValidationError("only a stdin attachment carries data; any other needs a ref",
					ErrorField{Field: field, Issue: "unsupported"})
			}
			refs = append(refs, agentsports.AttachmentRef{Kind: a.Kind, Ref: a.Ref})
			continue
		}
		switch {
		case stdin:
			return nil, ValidationError("a message has at most one stdin attachment", ErrorField{Field: field, Issue: "duplicate"})
		case a.DataB64 == nil || a.Ref != "":
			return nil, ValidationError("a stdin attachment carries data_b64 and no ref", ErrorField{Field: field, Issue: "unsupported"})
		case base64.StdEncoding.DecodedLen(len(*a.DataB64)) > agentsports.MaxStdinBytes+2:
			// Refused before decoding: a frame can hold 64 MiB.
			return nil, ValidationError(fmt.Sprintf("stdin is over %d bytes", agentsports.MaxStdinBytes), ErrorField{Field: field, Issue: "too_large"})
		}
		data, err := base64.StdEncoding.DecodeString(*a.DataB64)
		if err != nil {
			return nil, ValidationError("data_b64 is not base64", ErrorField{Field: field + "." + fieldDataB64, Issue: "invalid"})
		}
		if len(data) > agentsports.MaxStdinBytes {
			return nil, ValidationError(fmt.Sprintf("stdin is over %d bytes", agentsports.MaxStdinBytes), ErrorField{Field: field, Issue: "too_large"})
		}
		if data == nil {
			data = []byte{}
		}
		stdin = true
		refs = append(refs, agentsports.AttachmentRef{Kind: kindStdin, Data: data, Truncated: a.Truncated})
	}
	return refs, nil
}

type getThreadParams struct {
	ThreadID        string `json:"thread_id"`
	IncludeMessages bool   `json:"include_messages" api:"optional"`
	Limit           int    `json:"limit" api:"optional"`
	Cursor          string `json:"cursor" api:"optional"`
}

// threadResult is thread.get's answer: the Thread and, when asked for, a page of its
// messages, oldest first (API §5.23).
type threadResult struct {
	Thread
	Messages   *[]Message `json:"messages,omitempty"`
	NextCursor *string    `json:"next_cursor,omitempty"`
}

const (
	defaultMessagePage = 100
	maxMessagePage     = 1000
)

func handleThreadGet(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p getThreadParams
	if err := decode(raw, &p, "thread.get"); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, ValidationError("thread_id is required", ErrorField{Field: fieldThreadID, Issue: requiredTag})
	}
	if p.Limit < 0 || p.Limit > maxMessagePage {
		return nil, ValidationError(fmt.Sprintf("limit must be 1-%d", maxMessagePage), ErrorField{Field: "limit", Issue: "out_of_range"})
	}
	t, err := svc.Get(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out := threadResult{Thread: toWireThread(t)}
	if !p.IncludeMessages {
		return out, nil
	}
	msgs, err := svc.Messages(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	start := 0
	if p.Cursor != "" {
		start = -1
		for i, m := range msgs {
			if m.ID == p.Cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, ValidationError("cursor names no message of this thread", ErrorField{Field: "cursor", Issue: "unknown"})
		}
	}
	limit := p.Limit
	if limit == 0 {
		limit = defaultMessagePage
	}
	end := min(start+limit, len(msgs))
	page := make([]Message, 0, end-start)
	for _, m := range msgs[start:end] {
		page = append(page, toWireMessage(m))
	}
	out.Messages = &page
	if end < len(msgs) {
		next := msgs[end-1].ID
		out.NextCursor = &next
	}
	return out, nil
}

type threadListResult struct {
	Items []Thread `json:"items"`
}

func handleThreadList(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := threadListResult{Items: make([]Thread, 0, len(list))}
	for _, t := range list {
		out.Items = append(out.Items, toWireThread(t))
	}
	return out, nil
}

type updateThreadParams struct {
	ThreadID string  `json:"thread_id"`
	Mode     *string `json:"mode" api:"optional"`
	Model    *string `json:"model" api:"optional"`
	Title    *string `json:"title" api:"optional"`
}

func handleThreadUpdate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p updateThreadParams
	if err := decode(raw, &p, "thread.update"); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, ValidationError("thread_id is required", ErrorField{Field: fieldThreadID, Issue: requiredTag})
	}
	up := agentsdomain.UpdateParams{Model: p.Model, Title: p.Title}
	if p.Mode != nil {
		mode := secdomain.Mode(*p.Mode)
		up.Mode = &mode
	}
	t, err := svc.Update(ctx, p.ThreadID, up)
	if err != nil {
		return nil, err
	}
	return toWireThread(t), nil
}

type cancelThreadParams struct {
	ThreadID string `json:"thread_id"`
}

// cancelResult is thread.cancel's answer: when the turn ended, null when none was running.
type cancelResult struct {
	StoppedAt *int64 `json:"stopped_at"`
}

// handleThreadCancel is §5.21 (REQ-AGT-007): it returns once the turn has ended.
func handleThreadCancel(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := threads(c)
	if err != nil {
		return nil, err
	}
	var p cancelThreadParams
	if err := decode(raw, &p, "thread.cancel"); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, ValidationError("thread_id is required", ErrorField{Field: fieldThreadID, Issue: requiredTag})
	}
	at, err := svc.Cancel(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	return cancelResult{StoppedAt: at}, nil
}

// agentsDomainError maps the agent runtime's sentinels onto §3 (API §5.20, §5.22).
func agentsDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, agentsdomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	case errors.Is(err, agentsdomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, agentsdomain.ErrConflict):
		return codeConflict, domainConflict, true
	case errors.Is(err, agentsdomain.ErrBudgetExceeded):
		return codeBudgetExceeded, domainBudgetExceeded, true
	case errors.Is(err, agentsdomain.ErrProviderUnavailable):
		return codeProviderUnavailable, domainProviderUnavailable, true
	case errors.Is(err, agentsdomain.ErrThreadBlocked):
		return codeThreadBlocked, domainThreadBlocked, true
	default:
		return 0, "", false
	}
}
