package llmgw

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// script is how one model of a streamProvider behaves.
type script struct {
	// startErr refuses the call before any event.
	startErr error
	// hang delivers nothing until the call is cancelled: a first-token timeout.
	hang bool
	// holdOpen blocks Stream itself until the call is cancelled, as a server that never sends
	// its response headers does: the real adapters send the request inside Stream.
	holdOpen bool
	// delay comes before the first event.
	delay time.Duration
	// events are yielded in order, then err ends the stream when set.
	events []domain.Event
	err    error
	// hangAfter holds the stream open after events until the call is cancelled: a model
	// still generating. quietStop makes it then end without an error, which ports.Provider
	// allows an adapter that honours its context.
	hangAfter, quietStop bool
}

// streamProvider serves models by script and remembers every call it got.
type streamProvider struct {
	id      string
	local   bool
	scripts map[string]script

	mu       sync.Mutex
	requests []domain.Request
	threads  []string
}

func (p *streamProvider) ID() string  { return p.id }
func (p *streamProvider) Local() bool { return p.local }

func (p *streamProvider) Models(context.Context) ([]domain.Model, error) { return nil, nil }

func (p *streamProvider) Stream(ctx context.Context, req domain.Request) (iter.Seq2[domain.Event, error], error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.threads = append(p.threads, domain.ThreadOf(ctx))
	p.mu.Unlock()
	s := p.scripts[req.Model]
	if s.holdOpen {
		<-ctx.Done()
		return nil, &domain.ProviderError{Provider: p.id, Err: ctx.Err()}
	}
	if s.startErr != nil {
		return nil, s.startErr
	}
	return func(yield func(domain.Event, error) bool) {
		if s.hang {
			<-ctx.Done()
			yield(domain.Event{}, ctx.Err())
			return
		}
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-ctx.Done():
				yield(domain.Event{}, ctx.Err())
				return
			}
		}
		for _, ev := range s.events {
			if !yield(ev, nil) {
				return
			}
		}
		if s.err != nil {
			yield(domain.Event{}, s.err)
			return
		}
		if s.hangAfter {
			<-ctx.Done()
			if !s.quietStop {
				yield(domain.Event{}, ctx.Err())
			}
		}
	}, nil
}

func (p *streamProvider) called() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

type memUsage struct {
	mu   sync.Mutex
	recs []domain.UsageRecord
}

func (u *memUsage) Record(_ context.Context, rec domain.UsageRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.recs = append(u.recs, rec)
	return nil
}

func (u *memUsage) all() []domain.UsageRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]domain.UsageRecord(nil), u.recs...)
}

var answer = []domain.Event{
	{Kind: domain.EventTextDelta, Text: "hel"},
	{Kind: domain.EventTextDelta, Text: "lo"},
	{Kind: domain.EventUsage, Usage: domain.Usage{InputTokens: 1500, OutputTokens: 500}},
	{Kind: domain.EventDone, FinishReason: "stop"},
}

func status(code int) error {
	return &domain.ProviderError{Provider: "p", Status: code, Err: errors.New("refused")}
}

// rig is a catalog with the given providers and models, and a router over it.
type rig struct {
	catalog *Catalog
	router  *Router
	usage   *memUsage
}

func newRig(t *testing.T, offline bool, classes map[string][]string, providers []*streamProvider, models []domain.Model) rig {
	t.Helper()
	store := newMemStore()
	byProvider := map[string][]domain.Model{}
	for _, m := range models {
		if m.Health == "" {
			m.Health = domain.HealthOK
		}
		byProvider[m.Provider] = append(byProvider[m.Provider], m)
	}
	for id, ms := range byProvider {
		_ = store.Replace(context.Background(), id, ms)
	}
	c := newCatalog(store)
	entries := make([]Entry, 0, len(providers))
	for _, p := range providers {
		entries = append(entries, Entry{ID: p.id, Provider: p, Health: domain.HealthOK})
	}
	c.Configure(offline, entries)
	usage := &memUsage{}
	r, err := NewRouter(c, RouterConfig{
		Classes: classes, Usage: usage, Logger: quiet(),
		Redact:           func(s string) string { return strings.ReplaceAll(s, "sk-live-secret", "[REDACTED:openai_key]") },
		FirstTokenRemote: 150 * time.Millisecond,
		FirstTokenLocal:  300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rig{catalog: c, router: r, usage: usage}
}

func model(provider, name string) domain.Model {
	return domain.Model{
		ID: domain.QualifiedID(provider, name), Provider: provider,
		Caps: domain.Capabilities{Tools: true, ContextWindow: 128_000},
	}
}

// collect drains a routed call: its text, and the error that ended it.
func collect(t *testing.T, r *Router, call Call) (string, error) {
	t.Helper()
	// A deadline of its own, so a candidate that is never abandoned fails the test rather than
	// hanging the package.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := r.Stream(ctx, call)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for ev, err := range stream {
		if err != nil {
			return text.String(), err
		}
		if ev.Kind == domain.EventTextDelta {
			text.WriteString(ev.Text)
		}
	}
	return text.String(), nil
}

func hello(class string) Call {
	return Call{ThreadID: "th_1", TurnID: "tu_1", Class: class, Request: domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}}
}

// TestFallbackOn429_REQ_LLM_003: a 429, a 5xx inside the stream and a first token that does
// not arrive each move the call to the next candidate of the class, in declared order, and
// each failure is recorded in usage with its status.
func TestFallbackOn429_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	remote := &streamProvider{id: "or", scripts: map[string]script{
		"limited": {startErr: status(429)},
		"broken":  {events: nil, err: status(503)},
		"slow":    {hang: true},
		"mute":    {holdOpen: true},
	}}
	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{"good": {events: answer}}}
	r := newRig(t, false,
		map[string][]string{"code": {"or/limited", "or/broken", "or/slow", "or/mute", "ollama/good"}},
		[]*streamProvider{remote, local},
		[]domain.Model{model("or", "limited"), model("or", "broken"), model("or", "slow"), model("or", "mute"), model("ollama", "good")})

	start := time.Now()
	text, err := collect(t, r.router, hello("code"))
	if err != nil || text != "hello" {
		t.Fatalf("text %q, err %v; want the last candidate's answer", text, err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("the slow candidate was abandoned after %v, before its first-token timeout", waited)
	}
	got := make([]string, 0, len(r.usage.all()))
	for _, rec := range r.usage.all() {
		got = append(got, rec.ModelID+":"+rec.Status)
	}
	want := "or/limited:rate_limited,or/broken:error,or/slow:timeout,or/mute:timeout,ollama/good:ok"
	if strings.Join(got, ",") != want {
		t.Errorf("usage = %v, want %s", got, want)
	}
	for _, rec := range r.usage.all()[:4] {
		if rec.Error == "" {
			t.Errorf("failure %s recorded without its error", rec.ModelID)
		}
	}
}

// TestAPermanentFailureDoesNotFallBack_REQ_LLM_003: only 429, 5xx, a transport failure and a
// first-token timeout move to the next candidate. A 401 would fail the same way anywhere, so
// it ends the call — recorded — and the next candidate is never asked.
func TestAPermanentFailureDoesNotFallBack_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	a := &streamProvider{id: "a", scripts: map[string]script{"m": {startErr: status(401)}}}
	b := &streamProvider{id: "b", local: true, scripts: map[string]script{"m": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"a/m", "b/m"}},
		[]*streamProvider{a, b}, []domain.Model{model("a", "m"), model("b", "m")})

	_, err := collect(t, r.router, hello("code"))
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("err = %v, want the 401", err)
	}
	if b.called() != 0 {
		t.Error("a candidate was tried after a failure that is not retryable")
	}
	if recs := r.usage.all(); len(recs) != 1 || recs[0].Status != domain.UsageError {
		t.Errorf("usage = %+v, want the one failure", recs)
	}
}

// TestAFailureAfterTheFirstTokenIsReturned_REQ_LLM_003: once a candidate has delivered a token
// the caller has it, so a later failure ends the call instead of starting it again elsewhere.
func TestAFailureAfterTheFirstTokenIsReturned_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	a := &streamProvider{id: "a", scripts: map[string]script{"m": {
		events: []domain.Event{{Kind: domain.EventTextDelta, Text: "par"}}, err: status(502),
	}}}
	b := &streamProvider{id: "b", scripts: map[string]script{"m": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"a/m", "b/m"}},
		[]*streamProvider{a, b}, []domain.Model{model("a", "m"), model("b", "m")})

	text, err := collect(t, r.router, hello("code"))
	if text != "par" || err == nil {
		t.Fatalf("text %q, err %v; want the partial answer and the 502", text, err)
	}
	if b.called() != 0 {
		t.Error("the call was restarted on another candidate after a token was delivered")
	}
	recs := r.usage.all()
	if len(recs) != 1 || recs[0].Status != domain.UsageError || recs[0].FirstTokenMS == nil {
		t.Errorf("usage = %+v, want one error with its first-token time", recs)
	}
}

// TestACancelAfterTheFirstTokenIsAnError_REQ_AGT_007: a call cancelled while the model is
// still generating ends with the cancellation, never as a stream that finished. Found live
// against gpt-oss:20b: the pump dropped the error when the call's context was done, the
// caller saw a clean end, and a cancelled turn was recorded `end_turn`.
func TestACancelAfterTheFirstTokenIsAnError_REQ_AGT_007(t *testing.T) {
	t.Parallel()

	// An adapter that reports the cancellation races the pump for it; one that just stops
	// loses every time without the fix.
	for name, quiet := range map[string]bool{"reports it": false, "stops quietly": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := &streamProvider{id: "a", scripts: map[string]script{"m": {
				events: []domain.Event{{Kind: domain.EventTextDelta, Text: "par"}}, hangAfter: true, quietStop: quiet,
			}}}
			r := newRig(t, false, map[string][]string{"code": {"a/m"}},
				[]*streamProvider{a}, []domain.Model{model("a", "m")})

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stream, err := r.router.Stream(ctx, hello("code"))
			if err != nil {
				t.Fatal(err)
			}
			var end error
			for ev, err := range stream {
				if err != nil {
					end = err
					break
				}
				if ev.Kind == domain.EventTextDelta {
					cancel()
				}
			}
			if !errors.Is(end, context.Canceled) {
				t.Fatalf("the cancelled call ended with %v, want context.Canceled", end)
			}
			recs := r.usage.all()
			if len(recs) != 1 || recs[0].Status != domain.UsageError {
				t.Errorf("usage = %+v, want one error", recs)
			}
		})
	}
}

// TestEveryCandidateFailing_REQ_LLM_003: when the last candidate fails too, the call ends
// with ErrNoCandidate, which the API reports as PROVIDER_UNAVAILABLE.
func TestEveryCandidateFailing_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	a := &streamProvider{id: "a", scripts: map[string]script{"m": {startErr: status(500)}}}
	r := newRig(t, false, map[string][]string{"code": {"a/m"}},
		[]*streamProvider{a}, []domain.Model{model("a", "m")})

	if _, err := collect(t, r.router, hello("code")); !errors.Is(err, domain.ErrNoCandidate) {
		t.Errorf("err = %v, want ErrNoCandidate", err)
	}
}

// TestOfflineRejectsRemote_REQ_LLM_004: offline, a remote candidate is discarded before it is
// contacted; a local one still serves; with none local left the call is PROVIDER_UNAVAILABLE
// before anything is sent.
func TestOfflineRejectsRemote_REQ_LLM_004(t *testing.T) {
	t.Parallel()

	remote := &streamProvider{id: "or", scripts: map[string]script{"kimi": {events: answer}}}
	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{"gpt": {events: answer}}}
	r := newRig(t, true,
		map[string][]string{"code": {"or/kimi", "ollama/gpt"}, "plan": {"or/kimi"}},
		[]*streamProvider{remote, local},
		[]domain.Model{model("or", "kimi"), model("ollama", "gpt")})

	if text, err := collect(t, r.router, hello("code")); err != nil || text != "hello" {
		t.Fatalf("code: text %q, err %v; want the local model's answer", text, err)
	}
	if _, err := r.router.Stream(context.Background(), hello("plan")); !errors.Is(err, domain.ErrNoCandidate) {
		t.Errorf("plan: err = %v, want ErrNoCandidate before streaming", err)
	}
	if remote.called() != 0 {
		t.Error("a remote provider was contacted while offline")
	}
	if recs := r.usage.all(); len(recs) != 1 || recs[0].ModelID != "ollama/gpt" {
		t.Errorf("usage = %+v, want only the local call", recs)
	}
}

// TestUsageRecorded_REQ_LLM_005: every call records its thread and turn, model and provider,
// the input and output tokens, the time to the first token and the cost in micro-USD at the
// model's prices.
func TestUsageRecorded_REQ_LLM_005(t *testing.T) {
	t.Parallel()

	p := &streamProvider{id: "or", scripts: map[string]script{"kimi": {delay: 20 * time.Millisecond, events: answer}}}
	m := model("or", "kimi")
	m.PriceInMicroUSDPerMTok, m.PriceOutMicroUSDPerMTok = 570_000, 2_300_000
	r := newRig(t, false, map[string][]string{"code": {"or/kimi"}}, []*streamProvider{p}, []domain.Model{m})

	if _, err := collect(t, r.router, hello("code")); err != nil {
		t.Fatal(err)
	}
	recs := r.usage.all()
	if len(recs) != 1 {
		t.Fatalf("usage = %+v, want one row", recs)
	}
	rec := recs[0]
	if rec.ThreadID != "th_1" || rec.TurnID != "tu_1" || rec.ModelID != "or/kimi" || rec.Provider != "or" || rec.Status != domain.UsageOK {
		t.Errorf("row = %+v", rec)
	}
	if rec.InTokens != 1500 || rec.OutTokens != 500 {
		t.Errorf("tokens = %d in, %d out; want 1500 and 500", rec.InTokens, rec.OutTokens)
	}
	// 1500 × 0.57 + 500 × 2.3 = 855 + 1150 micro-USD.
	if rec.CostMicroUSD != 2005 {
		t.Errorf("cost = %d µUSD, want 2005", rec.CostMicroUSD)
	}
	if rec.FirstTokenMS == nil || *rec.FirstTokenMS < 20 {
		t.Errorf("first token = %v ms, want at least the 20 ms delay", rec.FirstTokenMS)
	}
	if rec.CreatedAt == 0 {
		t.Error("created_at not set")
	}
}

// TestTheCostRoundsToTheNearestMicroUSD_REQ_LLM_005 pins Model.Cost's arithmetic.
func TestTheCostRoundsToTheNearestMicroUSD_REQ_LLM_005(t *testing.T) {
	t.Parallel()

	m := domain.Model{PriceInMicroUSDPerMTok: 1_000_000, PriceOutMicroUSDPerMTok: 3_000_000}
	for _, c := range []struct{ in, out, want int64 }{
		{0, 0, 0}, {1, 0, 1}, {0, 1, 3}, {1000, 1000, 4000}, {1, 1, 4},
	} {
		if got := m.Cost(c.in, c.out); got != c.want {
			t.Errorf("Cost(%d, %d) = %d, want %d", c.in, c.out, got, c.want)
		}
	}
	half := domain.Model{PriceInMicroUSDPerMTok: 500_000}
	if got := half.Cost(1, 0); got != 1 {
		t.Errorf("half a micro-USD rounds to %d, want 1", got)
	}
	if got := half.Cost(0, 0); got != 0 {
		t.Errorf("nothing costs %d", got)
	}
	third := domain.Model{PriceInMicroUSDPerMTok: 400_000}
	if got := third.Cost(1, 0); got != 0 {
		t.Errorf("0.4 micro-USD rounds to %d, want 0", got)
	}
}

// TestCandidatesAreFilteredByCapabilityAndHealth_REQ_LLM_003: before declared order, a candidate
// is dropped when it cannot take the request's tools, its schema or its reasoning, when the
// request does not fit its window, and when it is down. One an adapter cannot carry is
// skipped without being recorded as a call.
func TestCandidatesAreFilteredByCapabilityAndHealth_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	p := &streamProvider{id: "p", local: true, scripts: map[string]script{
		"unsupported": {startErr: &domain.ProviderError{Provider: "p", Err: domain.ErrUnsupported}},
		"good":        {events: answer},
		"notools":     {events: answer},
	}}
	down := &streamProvider{id: "d", local: true, scripts: map[string]script{"m": {events: answer}}}

	// Each gated model fails exactly one filter, so each filter is tested on its own.
	full := func(name string) domain.Model {
		m := model("p", name)
		m.Caps.JSONSchema, m.Caps.Reasoning = true, true
		return m
	}
	noTools := full("notools")
	noTools.Caps.Tools = false
	small := full("small")
	small.Caps.ContextWindow = 1000
	noSchema := full("noschema")
	noSchema.Caps.JSONSchema = false
	noReasoning := full("noreasoning")
	noReasoning.Caps.Reasoning = false
	downModel := full("m")
	downModel.ID, downModel.Provider, downModel.Health = "d/m", "d", domain.HealthDown
	r := newRig(t, false,
		map[string][]string{"code": {"p/notools", "p/small", "p/noschema", "p/noreasoning", "d/m", "p/unsupported", "p/good"}},
		[]*streamProvider{p, down},
		[]domain.Model{noTools, small, noSchema, noReasoning, downModel, full("unsupported"), full("good")})

	call := hello("code")
	call.Request.Tools = []domain.ToolSpec{{Name: "read_file"}}
	call.Request.ResponseSchema = map[string]any{"type": "object"}
	call.Request.Reasoning = "high"
	call.Request.Messages[0].Text = strings.Repeat("word ", 2000)
	if text, err := collect(t, r.router, call); err != nil || text != "hello" {
		t.Fatalf("text %q, err %v", text, err)
	}
	asked := make([]string, 0, len(p.requests))
	for _, req := range p.requests {
		asked = append(asked, req.Model)
	}
	if strings.Join(asked, ",") != "unsupported,good" || down.called() != 0 {
		t.Errorf("asked %v and the down provider %d times; want only unsupported then good", asked, down.called())
	}
	if recs := r.usage.all(); len(recs) != 1 || recs[0].ModelID != "p/good" {
		t.Errorf("usage = %+v, want only the call that was made", recs)
	}

	// Without tools, schema or a long prompt, the first candidate serves.
	if _, err := collect(t, r.router, hello("code")); err != nil {
		t.Fatal(err)
	}
	if last := p.requests[len(p.requests)-1].Model; last != "notools" {
		t.Errorf("a plain call went to %s, want the first candidate", last)
	}
}

// TestALocalCandidateGetsTheLocalTimeout_REQ_LLM_003: a local model is given the longer
// first-token timeout (120 s against 30 s remote), since it may be loading.
func TestALocalCandidateGetsTheLocalTimeout_REQ_LLM_003(t *testing.T) {
	t.Parallel()

	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{
		"loading": {hang: true}, "good": {events: answer},
	}}
	r := newRig(t, false, map[string][]string{"code": {"ollama/loading", "ollama/good"}},
		[]*streamProvider{local}, []domain.Model{model("ollama", "loading"), model("ollama", "good")})

	start := time.Now()
	if _, err := collect(t, r.router, hello("code")); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 300*time.Millisecond {
		t.Errorf("a local candidate was abandoned after %v, before the local timeout", waited)
	}
}

// TestRedactionBeforeSending_REQ_SEC_001: all content reaches the provider redacted — the
// system prompt, every message, every tool call's arguments and result, every tool's
// description and input schema, and the response schema — and the caller's request is not
// changed.
func TestRedactionBeforeSending_REQ_SEC_001(t *testing.T) {
	t.Parallel()

	p := &streamProvider{id: "or", scripts: map[string]script{"m": {events: answer}}}
	m := model("or", "m")
	m.Caps.JSONSchema = true
	r := newRig(t, false, map[string][]string{"code": {"or/m"}}, []*streamProvider{p}, []domain.Model{m})

	const secret = "sk-live-secret"
	call := Call{Class: "code", Request: domain.Request{
		System: "key " + secret,
		Tools: []domain.ToolSpec{{
			Name: "deploy", Description: "uses " + secret,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"token": map[string]any{"type": "string", "default": secret, "enum": []any{secret, "other"}},
			}},
		}},
		ResponseSchema: map[string]any{"description": "answer without " + secret},
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "use " + secret},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "c1", Name: "run", Input: `{"env":"` + secret + `"}`}}},
			{Role: domain.RoleTool, ToolCallID: "c1", Text: "echo " + secret},
		},
	}}
	original := call.Request.Messages[0].Text
	if _, err := collect(t, r.router, call); err != nil {
		t.Fatal(err)
	}
	sent := p.requests[0]
	all := sent.System + sent.Tools[0].Description + fmt.Sprint(sent.Tools[0].InputSchema) + fmt.Sprint(sent.ResponseSchema)
	for _, m := range sent.Messages {
		all += m.Text
		for _, tc := range m.ToolCalls {
			all += tc.Input
		}
	}
	if strings.Contains(all, secret) || strings.Count(all, "[REDACTED:openai_key]") != 8 {
		t.Errorf("sent %q, want all eight copies redacted", all)
	}
	if call.Request.Messages[0].Text != original || !strings.Contains(fmt.Sprint(call.Request.Tools[0].InputSchema), secret) ||
		!strings.Contains(fmt.Sprint(call.Request.ResponseSchema), secret) {
		t.Error("redaction changed the caller's own request")
	}
}

// TestTheCallCarriesItsThread_REQ_SEC_002: the call's context names its thread, so the egress
// row of a remote request records it.
func TestTheCallCarriesItsThread_REQ_SEC_002(t *testing.T) {
	t.Parallel()

	p := &streamProvider{id: "or", scripts: map[string]script{"m": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"or/m"}}, []*streamProvider{p}, []domain.Model{model("or", "m")})
	if _, err := collect(t, r.router, hello("code")); err != nil {
		t.Fatal(err)
	}
	if p.threads[0] != "th_1" {
		t.Errorf("thread in the call's context = %q, want th_1", p.threads[0])
	}
}

// TestAnUnknownClassIsRefused covers a class models.toml does not declare, and a router
// built without the pieces it may not run without.
func TestAnUnknownClassIsRefused(t *testing.T) {
	t.Parallel()

	r := newRig(t, false, map[string][]string{}, nil, nil)
	if _, err := r.router.Stream(context.Background(), hello("code")); !errors.Is(err, domain.ErrUnknownClass) {
		t.Errorf("err = %v, want ErrUnknownClass", err)
	}
	if _, err := NewRouter(r.catalog, RouterConfig{Usage: &memUsage{}}); err == nil {
		t.Error("a router without a redactor was built")
	}
	if _, err := NewRouter(r.catalog, RouterConfig{Redact: func(s string) string { return s }}); err == nil {
		t.Error("a router without a usage log was built")
	}
	r.router.Configure(map[string][]string{"code": {}})
	if _, err := r.router.Stream(context.Background(), hello("code")); !errors.Is(err, domain.ErrNoCandidate) {
		t.Errorf("an empty class: err = %v, want ErrNoCandidate", err)
	}
}

// TestAThreadsModelIsItsOnlyCandidate_REQ_AGT_010: a call naming a model goes to that model,
// whatever its class would pick — a thread whose model was changed uses the new one.
func TestAThreadsModelIsItsOnlyCandidate_REQ_AGT_010(t *testing.T) {
	t.Parallel()
	p := &streamProvider{id: "ol", scripts: map[string]script{"a": {events: answer}, "b": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"ol/a"}}, []*streamProvider{p}, []domain.Model{model("ol", "a"), model("ol", "b")})

	call := hello("code")
	call.Model = "ol/b"
	if _, err := collect(t, r.router, call); err != nil {
		t.Fatal(err)
	}
	if got := p.requests[0].Model; got != "b" {
		t.Fatalf("the call went to %q, want the named model b", got)
	}
	call.Model = "ol/missing"
	if _, err := collect(t, r.router, call); !errors.Is(err, domain.ErrNoCandidate) {
		t.Fatalf("a named model that is not in the catalog: %v", err)
	}
}

// TestTheWindowIsTheSmallestCandidates_REQ_CTX_004: the budget a thread compacts to is the
// smallest known window among its candidates, so a fallback is not skipped for size.
func TestTheWindowIsTheSmallestCandidates_REQ_CTX_004(t *testing.T) {
	t.Parallel()
	big, small, unknown := model("ol", "big"), model("ol", "small"), model("ol", "unknown")
	small.Caps.ContextWindow = 8192
	unknown.Caps.ContextWindow = 0
	down := model("ol", "down")
	down.Caps.ContextWindow, down.Health = 1024, domain.HealthDown
	r := newRig(t, false, map[string][]string{"code": {"ol/big", "ol/unknown", "ol/small", "ol/down"}},
		[]*streamProvider{{id: "ol"}}, []domain.Model{big, small, unknown, down})

	if got := r.router.Window(t.Context(), "code", ""); got != 8192 {
		t.Fatalf("class window %d, want 8192", got)
	}
	if got := r.router.Window(t.Context(), "code", "ol/big"); got != 128_000 {
		t.Fatalf("a named model's window %d, want its own", got)
	}
	if got := r.router.Window(t.Context(), "code", "ol/unknown"); got != 0 {
		t.Fatalf("an unknown window is 0, got %d", got)
	}
}

// TestTheUsageEventCarriesModelAndCost_REQ_LLM_005: the caller learns which model served the
// call and what it cost, which a thread adds to its own total.
func TestTheUsageEventCarriesModelAndCost_REQ_LLM_005(t *testing.T) {
	t.Parallel()
	p := &streamProvider{id: "or", scripts: map[string]script{"kimi": {events: answer}}}
	m := model("or", "kimi")
	m.PriceInMicroUSDPerMTok, m.PriceOutMicroUSDPerMTok = 570_000, 2_300_000
	r := newRig(t, false, map[string][]string{"code": {"or/kimi"}}, []*streamProvider{p}, []domain.Model{m})
	stream, err := r.router.Stream(t.Context(), hello("code"))
	if err != nil {
		t.Fatal(err)
	}
	var usage domain.Usage
	for ev, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == domain.EventUsage {
			usage = ev.Usage
		}
	}
	if usage.Model != "or/kimi" || usage.CostMicroUSD != 2005 {
		t.Fatalf("usage event %+v", usage)
	}
}

// TestAvailableSaysWhetherAnyCandidateCanServe_REQ_LLM_003: thread.send refuses a thread with
// no candidate up front (PROVIDER_UNAVAILABLE) rather than start a turn that cannot run.
func TestAvailableSaysWhetherAnyCandidateCanServe_REQ_LLM_003(t *testing.T) {
	t.Parallel()
	up, down := model("ol", "up"), model("ol", "down")
	down.Health = domain.HealthDown
	r := newRig(t, false, map[string][]string{"code": {"ol/down", "ol/up"}, "plan": {"ol/down"}},
		[]*streamProvider{{id: "ol"}}, []domain.Model{up, down})
	for _, c := range []struct {
		class, model string
		want         bool
	}{
		{"code", "", true},
		{"plan", "", false},
		{"nope", "", false},
		{"code", "ol/up", true},
		{"code", "ol/down", false},
		{"code", "ol/missing", false},
	} {
		if got := r.router.Available(t.Context(), c.class, c.model); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.class, c.model, got, c.want)
		}
	}
}
