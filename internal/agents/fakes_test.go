package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	"github.com/ecrespo/umbral/internal/bus"
	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	toolsdomain "github.com/ecrespo/umbral/internal/tools/domain"
)

// memStore is ports.Store in memory. failAfter makes every write past the n-th fail.
type memStore struct {
	mu        sync.Mutex
	threads   map[string]domain.Thread
	messages  []domain.Message
	calls     map[string]domain.ToolCall
	callOrder []string
	rules     []secdomain.Rule
	approvals []domain.Approval
	writes    int
	failAfter int
	// beforeBegin runs between the send's read of the thread and BeginTurn's.
	beforeBegin func()
	// beforeFinish runs in the turn's goroutine as FinishTurn starts, before it writes.
	beforeFinish func()
}

func newMemStore() *memStore {
	return &memStore{threads: map[string]domain.Thread{}, calls: map[string]domain.ToolCall{}}
}

func (s *memStore) write() error {
	s.writes++
	if s.failAfter > 0 && s.writes > s.failAfter {
		return errors.New("disk full")
	}
	return nil
}

func (s *memStore) CreateThread(_ context.Context, t domain.Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[t.ID] = t
	return nil
}

func (s *memStore) Thread(_ context.Context, id string) (domain.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[id]
	if !ok {
		return domain.Thread{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	return t, nil
}

func (s *memStore) Threads(context.Context) ([]domain.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Thread, 0, len(s.threads))
	for _, t := range s.threads {
		if !t.Ephemeral {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func (s *memStore) UpdateThread(_ context.Context, id string, p domain.UpdateParams, now int64) (domain.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[id]
	if !ok {
		return domain.Thread{}, domain.ErrNotFound
	}
	if p.Mode != nil {
		if t.State == domain.StateRunning {
			return domain.Thread{}, domain.ErrConflict
		}
		t.Mode = *p.Mode
	}
	if p.Model != nil {
		t.Model = *p.Model
	}
	if p.Title != nil {
		t.Title = *p.Title
	}
	t.UpdatedAt = now
	s.threads[id] = t
	return t, nil
}

func (s *memStore) BeginTurn(_ context.Context, msg domain.Message, now int64) (domain.Message, domain.Thread, error) {
	if s.beforeBegin != nil {
		s.beforeBegin()
	}
	// The duplicate check and the write are two steps here, with room for a concurrent
	// BeginTurn in between: the real store makes them one IMMEDIATE transaction, and the
	// runtime's per-thread lock must hold on its own too.
	s.mu.Lock()
	for _, m := range s.messages {
		if msg.ClientMsgID != "" && m.ThreadID == msg.ThreadID && m.ClientMsgID == msg.ClientMsgID {
			s.mu.Unlock()
			return m, domain.Thread{}, ports.ErrDuplicate
		}
	}
	s.mu.Unlock()
	time.Sleep(time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[msg.ThreadID]
	switch {
	case !ok:
		return domain.Message{}, domain.Thread{}, domain.ErrNotFound
	case t.State == domain.StateRunning:
		return domain.Message{}, domain.Thread{}, domain.ErrConflict
	case t.TokensUsed >= t.BudgetTokens:
		return domain.Message{}, domain.Thread{}, domain.ErrBudgetExceeded
	}
	if err := s.write(); err != nil {
		return domain.Message{}, domain.Thread{}, err
	}
	s.messages = append(s.messages, msg)
	t.State, t.UpdatedAt = domain.StateRunning, now
	s.threads[t.ID] = t
	return msg, t, nil
}

func (s *memStore) AppendMessage(_ context.Context, msg domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(); err != nil {
		return err
	}
	s.messages = append(s.messages, msg)
	return nil
}

func (s *memStore) AppendContent(_ context.Context, id, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(); err != nil {
		return err
	}
	for i := range s.messages {
		if s.messages[i].ID == id {
			s.messages[i].Content += text
			return nil
		}
	}
	return errors.New("no such message")
}

func (s *memStore) SaveToolCall(_ context.Context, c domain.ToolCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(); err != nil {
		return err
	}
	if _, ok := s.calls[c.ID]; !ok {
		s.callOrder = append(s.callOrder, c.ID)
	}
	s.calls[c.ID] = c
	return nil
}

func (s *memStore) FinishTurn(_ context.Context, id string, state domain.State, u domain.Usage, now int64) error {
	if s.beforeFinish != nil {
		s.beforeFinish()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.threads[id]
	t.State, t.UpdatedAt = state, now
	t.TokensUsed += u.InTokens + u.OutTokens
	t.CostMicroUSD += u.CostMicroUSD
	s.threads[id] = t
	return nil
}

func (s *memStore) MessageByClientID(_ context.Context, threadID, clientID string) (domain.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m.ThreadID == threadID && m.ClientMsgID == clientID {
			return m, true, nil
		}
	}
	return domain.Message{}, false, nil
}

func (s *memStore) Messages(_ context.Context, threadID string) ([]domain.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Message
	for _, m := range s.messages {
		if m.ThreadID == threadID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memStore) ToolCalls(_ context.Context, threadID string) ([]domain.ToolCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ToolCall
	for _, id := range s.callOrder {
		if c := s.calls[id]; c.ThreadID == threadID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *memStore) Rules(context.Context, string) ([]secdomain.Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]secdomain.Rule(nil), s.rules...), nil
}

func (s *memStore) RequestApproval(_ context.Context, a domain.Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(); err != nil {
		return err
	}
	s.approvals = append(s.approvals, a)
	t := s.threads[a.ThreadID]
	t.State = domain.StateAwaitingApproval
	s.threads[a.ThreadID] = t
	return nil
}

func (s *memStore) Approval(_ context.Context, id string) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.approvals {
		if a.ID == id {
			return a, nil
		}
	}
	return domain.Approval{}, domain.ErrNotFound
}

func (s *memStore) Approvals(_ context.Context, threadID string, all bool) ([]domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Approval
	for _, a := range s.approvals {
		if (all || a.State == domain.ApprovalPending) && (threadID == "" || a.ThreadID == threadID) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *memStore) DecideApproval(_ context.Context, id string, state domain.ApprovalState, scope domain.Scope, rule *secdomain.Rule, now int64) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.approvals {
		if a.ID != id {
			continue
		}
		if a.State != domain.ApprovalPending {
			return domain.Approval{}, domain.ErrConflict
		}
		if err := s.write(); err != nil {
			return domain.Approval{}, err
		}
		a.State, a.Scope, a.DecidedAt = state, scope, &now
		s.approvals[i] = a
		if rule != nil {
			s.rules = append(s.rules, *rule)
		}
		t := s.threads[a.ThreadID]
		if t.State == domain.StateAwaitingApproval {
			t.State = domain.StateRunning
			s.threads[a.ThreadID] = t
		}
		return a, nil
	}
	return domain.Approval{}, domain.ErrNotFound
}

// scripted is the model gateway: each call takes the next script, and remembers its request.
type scripted struct {
	mu      sync.Mutex
	scripts [][]llm.Event
	calls   []ports.ModelCall
	window  int64
	// hold, when set, keeps every stream from starting until it is closed.
	hold chan struct{}
	// unavailable makes Available report no candidate.
	unavailable bool
}

// answer is a script that says text and ends.
func answer(text string) []llm.Event {
	return []llm.Event{
		{Kind: llm.EventTextDelta, Text: text[:len(text)/2]},
		{Kind: llm.EventTextDelta, Text: text[len(text)/2:]},
		{Kind: llm.EventUsage, Usage: llm.Usage{InputTokens: 100, OutputTokens: 20, CostMicroUSD: 7}},
		{Kind: llm.EventDone, FinishReason: "stop"},
	}
}

// callTool is a script that asks for one tool.
func callTool(name, input string) []llm.Event {
	return []llm.Event{
		{Kind: llm.EventToolCall, ToolCall: llm.ToolCall{ID: "prov_1", Name: name, Input: input}},
		{Kind: llm.EventUsage, Usage: llm.Usage{InputTokens: 50, OutputTokens: 10}},
		{Kind: llm.EventDone, FinishReason: "tool_calls"},
	}
}

func (m *scripted) Stream(ctx context.Context, call ports.ModelCall) (iter.Seq2[llm.Event, error], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if len(m.scripts) == 0 {
		return nil, fmt.Errorf("%w: no script left", llm.ErrNoCandidate)
	}
	events := m.scripts[0]
	m.scripts = m.scripts[1:]
	hold := m.hold
	return func(yield func(llm.Event, error) bool) {
		if hold != nil {
			// A provider's stream ends when its call's context does.
			select {
			case <-hold:
			case <-ctx.Done():
				yield(llm.Event{}, ctx.Err())
				return
			}
		}
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}, nil
}

func (m *scripted) Window(context.Context, string, string) int64 { return m.window }

func (m *scripted) Available(context.Context, string, string) bool { return !m.unavailable }

func (m *scripted) requests() []ports.ModelCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ports.ModelCall(nil), m.calls...)
}

// fakeTool is one tool of fakeTools.
type fakeTool struct {
	risk secdomain.Risk
	ran  atomic.Int32
	out  string
	// optional makes "path" optional: the tool accepts {}.
	optional bool
	// actionErr, when set, is what Action returns for every call.
	actionErr error
}

// fakeTools is the tool registry: every tool takes {"path": …}.
type fakeTools struct {
	tools map[string]*fakeTool
}

func (f *fakeTools) Specs() []toolsdomain.Spec {
	out := make([]toolsdomain.Spec, 0, len(f.tools))
	for name, t := range f.tools {
		out = append(out, toolsdomain.Spec{
			Name: name, Risk: t.risk, Description: name,
			InputSchema: map[string]any{"type": "object"},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *fakeTools) Action(env toolsdomain.Env, call toolsdomain.Call) (secdomain.Action, error) {
	t, ok := f.tools[call.Tool]
	if !ok {
		return secdomain.Action{}, toolsdomain.ErrUnknownTool
	}
	if t.actionErr != nil {
		return secdomain.Action{}, t.actionErr
	}
	var in struct{ Path string }
	if err := json.Unmarshal(call.Input, &in); err != nil || (in.Path == "" && !t.optional) {
		return secdomain.Action{}, toolsdomain.ErrInvalidInput
	}
	return secdomain.Action{ThreadID: env.ThreadID, Tool: call.Tool, Risk: t.risk, Target: in.Path, Cwd: env.Cwd, WriteRoot: env.WriteRoot}, nil
}

func (f *fakeTools) Preview(context.Context, toolsdomain.Env, toolsdomain.Call) (string, error) {
	return "", nil
}

func (f *fakeTools) Invoke(_ context.Context, _ toolsdomain.Env, call toolsdomain.Call, grant toolsdomain.Grant) (toolsdomain.Result, error) {
	if !grant.Allows() {
		return toolsdomain.Result{}, toolsdomain.ErrNotAuthorized
	}
	t := f.tools[call.Tool]
	t.ran.Add(1)
	return toolsdomain.Result{Text: t.out, Summary: "done"}, nil
}

// checkingBus is the publisher: at the moment an event is published it checks that what the
// event reports is already in the store (DD-007), then records it.
type checkingBus struct {
	t         *testing.T
	store     *memStore
	mu        sync.Mutex
	evs       []bus.Event
	fail      []string
	ended     chan ports.TurnFinished
	approvals chan domain.Approval
	// onApproval runs in the turn's goroutine as approval.requested is published.
	onApproval func(domain.Approval)
}

func (b *checkingBus) Publish(ev bus.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch e := ev.(type) {
	case ports.ThreadDelta:
		if e.Kind == "text" && !b.persisted(e) {
			b.fail = append(b.fail, fmt.Sprintf("delta %q published before it was persisted", e.Text))
		}
	case ports.ThreadToolCall:
		b.store.mu.Lock()
		stored, ok := b.store.calls[e.Call.ID]
		b.store.mu.Unlock()
		if !ok || stored.Status != e.Call.Status {
			b.fail = append(b.fail, fmt.Sprintf("tool call %s %s published before it was persisted", e.Call.ID, e.Call.Status))
		}
	case ports.ContextCompacted:
		found := false
		b.store.mu.Lock()
		for _, m := range b.store.messages {
			found = found || m.Role == domain.RoleSystemNote
		}
		b.store.mu.Unlock()
		if !found {
			b.fail = append(b.fail, "context.compacted published before its summary was persisted")
		}
	case ports.ApprovalRequested:
		b.store.mu.Lock()
		found := false
		for _, a := range b.store.approvals {
			found = found || a.ID == e.Approval.ID
		}
		state := b.store.threads[e.Approval.ThreadID].State
		b.store.mu.Unlock()
		if !found || state != domain.StateAwaitingApproval {
			b.fail = append(b.fail, "approval.requested published before the approval and the paused thread were persisted")
		}
		defer func() {
			if b.onApproval != nil {
				b.onApproval(e.Approval)
			}
			if b.approvals != nil {
				b.approvals <- e.Approval
			}
		}()
	case ports.TurnFinished:
		b.store.mu.Lock()
		state := b.store.threads[e.ThreadID].State
		b.store.mu.Unlock()
		if state == domain.StateRunning {
			b.fail = append(b.fail, "turn_finished published while the thread is still running")
		}
		defer func() { b.ended <- e }()
	}
	b.evs = append(b.evs, ev)
}

// persisted reports whether the assistant message of the turn already holds everything the
// deltas so far said, this one included.
func (b *checkingBus) persisted(e ports.ThreadDelta) bool {
	var said strings.Builder
	for _, ev := range b.evs {
		if d, ok := ev.(ports.ThreadDelta); ok && d.TurnID == e.TurnID && d.Kind == "text" {
			said.WriteString(d.Text)
		}
	}
	said.WriteString(e.Text)
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for _, m := range b.store.messages {
		if m.TurnID == e.TurnID && m.Role == domain.RoleAssistant && strings.HasSuffix(m.Content, said.String()) {
			return true
		}
	}
	return false
}

func (b *checkingBus) events() []bus.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bus.Event(nil), b.evs...)
}

// fakeMetrics is ports.Metrics: it counts invalid tool calls by model.
type fakeMetrics struct {
	mu      sync.Mutex
	invalid map[string]int
}

func (m *fakeMetrics) ToolCallInvalid(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.invalid == nil {
		m.invalid = map[string]int{}
	}
	m.invalid[model]++
}

func (m *fakeMetrics) count(model string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.invalid[model]
}

type rig struct {
	rt      *Runtime
	store   *memStore
	models  *scripted
	tools   *fakeTools
	bus     *checkingBus
	metrics *fakeMetrics
}

var ids atomic.Int64

func newID(prefix string) string {
	// Increasing, so the store's order is creation order.
	return fmt.Sprintf("%s_%026d", prefix, ids.Add(1))
}

func newRig(t *testing.T, scripts ...[]llm.Event) *rig {
	t.Helper()
	store := newMemStore()
	b := &checkingBus{t: t, store: store, ended: make(chan ports.TurnFinished, 16), approvals: make(chan domain.Approval, 16)}
	models := &scripted{scripts: scripts}
	tools := &fakeTools{tools: map[string]*fakeTool{
		"read_file":  {risk: secdomain.RiskReadOnly, out: "file contents"},
		"write_file": {risk: secdomain.RiskWriteFS, out: "written"},
		"run":        {risk: secdomain.RiskExec, out: "ran"},
		"list_dir":   {risk: secdomain.RiskReadOnly, out: "a b c", optional: true},
		"broken_env": {risk: secdomain.RiskWriteFS, actionErr: toolsdomain.ErrInvalidEnv},
	}}
	metrics := &fakeMetrics{}
	rt, err := New(t.Context(), Config{
		Store: store, Models: models, Tools: tools, Bus: b, NewID: newID, Metrics: metrics,
		IsRepo: func(string) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rt.Close()
		for _, f := range b.fail {
			t.Error(f)
		}
	})
	return &rig{rt: rt, store: store, models: models, tools: tools, bus: b, metrics: metrics}
}

func (r *rig) thread(t *testing.T, p domain.CreateParams) domain.Thread {
	t.Helper()
	if p.Cwd == "" {
		p.Cwd = t.TempDir()
	}
	th, err := r.rt.Create(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// send sends text and waits for the turn to finish.
func (r *rig) send(t *testing.T, threadID, text, clientID string) (ports.SendResult, ports.TurnFinished) {
	t.Helper()
	res, err := r.rt.Send(t.Context(), ports.SendParams{ThreadID: threadID, Text: text, ClientMsgID: clientID})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case end := <-r.bus.ended:
		return res, end
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish")
	}
	return res, ports.TurnFinished{}
}
