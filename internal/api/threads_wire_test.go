package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
)

type fakeThreads struct {
	thread   agentsdomain.Thread
	messages []agentsdomain.Message
	sent     []agentsports.SendParams
	sendErr  error
	updated  agentsdomain.UpdateParams
}

func (f *fakeThreads) Create(_ context.Context, p agentsdomain.CreateParams) (agentsdomain.Thread, error) {
	if _, err := p.Validate(); err != nil {
		return agentsdomain.Thread{}, err
	}
	return f.thread, nil
}

func (f *fakeThreads) Send(_ context.Context, p agentsports.SendParams) (agentsports.SendResult, error) {
	f.sent = append(f.sent, p)
	if f.sendErr != nil {
		return agentsports.SendResult{}, f.sendErr
	}
	return agentsports.SendResult{TurnID: "trn_1", MessageID: "msg_1"}, nil
}

func (f *fakeThreads) Get(_ context.Context, id string) (agentsdomain.Thread, error) {
	if id != f.thread.ID {
		return agentsdomain.Thread{}, fmt.Errorf("%w: %s", agentsdomain.ErrNotFound, id)
	}
	return f.thread, nil
}

func (f *fakeThreads) List(context.Context) ([]agentsdomain.Thread, error) {
	return []agentsdomain.Thread{f.thread}, nil
}

func (f *fakeThreads) Update(_ context.Context, _ string, p agentsdomain.UpdateParams) (agentsdomain.Thread, error) {
	f.updated = p
	return f.thread, nil
}

func (f *fakeThreads) Messages(context.Context, string) ([]agentsdomain.Message, error) {
	return f.messages, nil
}

func threadRig(t *testing.T) (*fakeThreads, *client, *bus.Bus) {
	t.Helper()
	svc := &fakeThreads{thread: agentsdomain.Thread{
		ID: "thr_1", Mode: "normal", ModelClass: "code", Cwd: "/w", State: agentsdomain.StateIdle,
		AttentionState: "idle", MaxSteps: 50, BudgetTokens: 400000, CreatedAt: 1, UpdatedAt: 2,
	}}
	for i := range 5 {
		svc.messages = append(svc.messages, agentsdomain.Message{ID: fmt.Sprint("msg_", i), ThreadID: "thr_1", TurnID: "trn_1", Role: agentsdomain.RoleUser, Content: fmt.Sprint("m", i)})
	}
	b := bus.New()
	s := testServerWithConfig(t, Config{Threads: svc, Bus: b})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Notify(ctx)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	return svc, c, b
}

// TestUmbReachesOnlyCreateAndSend_REQ_API_001: §2's cli row gives `umb` thread.create and
// thread.send; get, list and update are the interactive clients'.
func TestUmbReachesOnlyCreateAndSend_REQ_API_001(t *testing.T) {
	t.Parallel()
	svc := &fakeThreads{thread: agentsdomain.Thread{ID: "thr_1", Mode: "normal", ModelClass: "code", Cwd: "/w"}}
	s := testServerWithConfig(t, Config{Threads: svc, Bus: bus.New()})
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	if resp := c.call(2, "thread.create", map[string]any{"cwd": "/w"}); resp.Error != nil {
		t.Fatalf("thread.create from umb = %+v", resp.Error)
	}
	if resp := c.call(3, "thread.send", map[string]any{"thread_id": "thr_1", "text": "x"}); resp.Error != nil {
		t.Fatalf("thread.send from umb = %+v", resp.Error)
	}
	for i, m := range []string{"thread.get", "thread.list", "thread.update"} {
		if resp := c.call(4+i, m, map[string]any{"thread_id": "thr_1", "mode": "auto-edit"}); resp.Error == nil || resp.Error.Code != codeMethodNotFound {
			t.Errorf("%s from umb = %+v, want METHOD_NOT_FOUND", m, resp.Error)
		}
	}
	if svc.updated.Mode != nil {
		t.Fatal("umb changed a thread's mode")
	}
}

// TestThreadMethodsReachTheWire_REQ_AGT_001: thread.* answers API §4's shapes to an
// interactive client and maps the runtime's errors onto §3.
func TestThreadMethodsReachTheWire_REQ_AGT_001(t *testing.T) {
	t.Parallel()
	svc, c, _ := threadRig(t)

	resp := c.call(2, "thread.create", map[string]any{"cwd": "/w"})
	raw, _ := json.Marshal(resp.Result)
	if resp.Error != nil || !strings.Contains(string(raw), `"id":"thr_1"`) || !strings.Contains(string(raw), `"model":null`) {
		t.Fatalf("thread.create = %s %+v", raw, resp.Error)
	}
	if resp := c.call(3, "thread.create", map[string]any{}); resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Fatalf("thread.create without cwd = %+v", resp.Error)
	}

	resp = c.call(4, "thread.send", map[string]any{
		"thread_id": "thr_1", "text": "fix it",
		"client_msg_id": "01J9Z3K8T2QH6W4V5X7Y8Z9A0B", "attachments": []map[string]any{{"kind": "block", "ref": "blk_1"}},
	})
	raw, _ = json.Marshal(resp.Result)
	if resp.Error != nil || !strings.Contains(string(raw), `"turn_id":"trn_1"`) || !strings.Contains(string(raw), `"message_id":"msg_1"`) {
		t.Fatalf("thread.send = %s %+v", raw, resp.Error)
	}
	if p := svc.sent[0]; p.ClientMsgID != "01J9Z3K8T2QH6W4V5X7Y8Z9A0B" || p.Attachments[0].Ref != "blk_1" || p.Text != "fix it" {
		t.Fatalf("the runtime got %+v", p)
	}
	if resp := c.call(5, "thread.send", map[string]any{"thread_id": "thr_1", "text": "x", "wait": map[string]any{"until": []string{"idle"}}}); resp.Error == nil || resp.Error.Code != codeNotImplemented {
		t.Fatalf("thread.send with wait = %+v, want NOT_IMPLEMENTED", resp.Error)
	}
	if len(svc.sent) != 1 {
		t.Fatal("a send with a wait reached the runtime")
	}
	for code, err := range map[int]error{
		codeConflict:            agentsdomain.ErrConflict,
		codeBudgetExceeded:      agentsdomain.ErrBudgetExceeded,
		codeNotFound:            agentsdomain.ErrNotFound,
		codeProviderUnavailable: agentsdomain.ErrProviderUnavailable,
	} {
		svc.sendErr = err
		if resp := c.call(6, "thread.send", map[string]any{"thread_id": "thr_1", "text": "x"}); resp.Error == nil || resp.Error.Code != code {
			t.Errorf("%v = %+v, want %d", err, resp.Error, code)
		}
	}

	resp = c.call(7, "thread.get", map[string]any{"thread_id": "thr_1", "include_messages": true, "limit": 2})
	var page struct {
		ID         string    `json:"id"`
		Messages   []Message `json:"messages"`
		NextCursor *string   `json:"next_cursor"`
	}
	decodeResult(t, resp, &page)
	if page.ID != "thr_1" || len(page.Messages) != 2 || page.NextCursor == nil || *page.NextCursor != "msg_1" {
		t.Fatalf("thread.get page = %+v", page)
	}
	resp = c.call(8, "thread.get", map[string]any{"thread_id": "thr_1", "include_messages": true, "cursor": "msg_3"})
	page.NextCursor, page.Messages = nil, nil
	decodeResult(t, resp, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != "msg_4" || page.NextCursor != nil {
		t.Fatalf("the last page = %+v", page)
	}
	if resp := c.call(9, "thread.get", map[string]any{"thread_id": "thr_x"}); resp.Error == nil || resp.Error.Code != codeNotFound {
		t.Fatalf("unknown thread = %+v", resp.Error)
	}

	if resp := c.call(10, "thread.list", nil); resp.Error != nil {
		t.Fatalf("thread.list = %+v", resp.Error)
	}
	if resp := c.call(11, "thread.update", map[string]any{"thread_id": "thr_1", "model": "ollama/b"}); resp.Error != nil ||
		svc.updated.Model == nil || *svc.updated.Model != "ollama/b" || svc.updated.Mode != nil {
		t.Fatalf("thread.update = %+v, runtime got %+v", resp.Error, svc.updated)
	}
}

// TestThreadNotificationsReachTheWire_REQ_AGT_001: the runtime's events arrive as §6's
// thread.delta, thread.tool_call, thread.turn_finished and context.compacted.
func TestThreadNotificationsReachTheWire_REQ_AGT_001(t *testing.T) {
	t.Parallel()
	_, c, b := threadRig(t)
	// A call first, so the connection is registered before the events go out.
	if resp := c.call(2, "thread.list", nil); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	b.Publish(agentsports.ThreadDelta{ThreadID: "thr_1", TurnID: "trn_1", Kind: "text", Text: "hel"})
	b.Publish(agentsports.ThreadToolCall{Call: agentsdomain.ToolCall{
		ID: "tc_1", ThreadID: "thr_1", MessageID: "msg_2",
		Tool: "read_file", Risk: "ReadOnly", Args: json.RawMessage(`{"path":"a"}`), Status: agentsdomain.ToolOK, StartedAt: 3,
	}})
	b.Publish(agentsports.ContextCompacted{ThreadID: "thr_1", BeforeTokens: 900, AfterTokens: 300})
	b.Publish(agentsports.TurnFinished{
		ThreadID: "thr_1", TurnID: "trn_1", StopReason: agentsdomain.StopEndTurn,
		Usage: agentsdomain.Usage{InTokens: 10, OutTokens: 2, CostMicroUSD: 5},
	})

	want := []struct{ method, contains string }{
		{"thread.delta", `"text":"hel"`},
		{"thread.tool_call", `"args":{"path":"a"}`},
		{"context.compacted", `"before_tokens":900`},
		{"thread.turn_finished", `"usage":{"in_tokens":10,"out_tokens":2,"cost_micro_usd":5}`},
	}
	for _, w := range want {
		method, _, params := c.readNotification(t)
		if method != w.method || !strings.Contains(string(params), w.contains) {
			t.Fatalf("got %s %s, want %s with %s", method, params, w.method, w.contains)
		}
	}
}

// TestThreadsWithoutARuntimeAreNotImplemented_REQ_API_003: a daemon with no runtime wired
// answers NOT_IMPLEMENTED and does not advertise `threads`.
func TestThreadsWithoutARuntimeAreNotImplemented_REQ_API_003(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	resp := c.hello(s.Token(), ClientTUI)
	if raw, _ := json.Marshal(resp.Result); strings.Contains(string(raw), `"threads"`) {
		t.Fatalf("hello advertises threads with no runtime: %s", raw)
	}
	if resp := c.call(2, "thread.list", nil); resp.Error == nil || resp.Error.Code != codeNotImplemented {
		t.Fatalf("thread.list = %+v", resp.Error)
	}
}
