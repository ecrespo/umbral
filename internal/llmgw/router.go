package llmgw

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/llmgw/ports"
)

// First-token timeouts of REQ-LLM-003: how long a candidate has to deliver its first event
// before the call moves on to the next one.
const (
	FirstTokenTimeoutRemote = 30 * time.Second
	FirstTokenTimeoutLocal  = 120 * time.Second
)

// RouterConfig is what a Router needs. Redact and Usage are required: a router that could
// send unredacted content, or make a call nobody records, is not built.
type RouterConfig struct {
	// Classes maps a task class (fast, code, plan, embed) to its candidates in declared order,
	// each `<provider>/<model>` (DD-004).
	Classes map[string][]string
	// Redact applies the redaction rules to one piece of content (REQ-SEC-001).
	Redact func(string) string
	Usage  ports.UsageLog
	Logger *slog.Logger
	// FirstTokenRemote and FirstTokenLocal override the REQ-LLM-003 timeouts; zero keeps them.
	FirstTokenRemote, FirstTokenLocal time.Duration
}

// Router walks a task class's candidates (DD-004): offline filter, capability filter, health,
// then declared order, falling back on 429, 5xx, a transport failure or a first token that
// does not arrive (REQ-LLM-003). Every call is recorded in usage (REQ-LLM-005), the request is
// redacted before any candidate sees it (REQ-SEC-001), and the call's context names its
// thread for the egress log (REQ-SEC-002).
type Router struct {
	catalog *Catalog
	cfg     RouterConfig
	now     func() time.Time

	mu      sync.RWMutex
	classes map[string][]string
}

// Call is one routed model call: the class picks the candidates, so Request.Model is ignored.
type Call struct {
	ThreadID string
	TurnID   string
	Class    string
	Request  domain.Request
}

// NewRouter builds a router over the catalog.
func NewRouter(catalog *Catalog, cfg RouterConfig) (*Router, error) {
	if catalog == nil {
		return nil, errors.New("llmgw: a router needs a catalog")
	}
	if cfg.Redact == nil {
		return nil, errors.New("llmgw: a router needs a redactor (REQ-SEC-001)")
	}
	if cfg.Usage == nil {
		return nil, errors.New("llmgw: a router needs a usage log (REQ-LLM-005)")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.FirstTokenRemote <= 0 {
		cfg.FirstTokenRemote = FirstTokenTimeoutRemote
	}
	if cfg.FirstTokenLocal <= 0 {
		cfg.FirstTokenLocal = FirstTokenTimeoutLocal
	}
	r := &Router{catalog: catalog, cfg: cfg, now: time.Now}
	r.Configure(cfg.Classes)
	return r, nil
}

// Configure replaces the classes, at start and on config.reload.
func (r *Router) Configure(classes map[string][]string) {
	copied := make(map[string][]string, len(classes))
	for class, candidates := range classes {
		copied[class] = append([]string(nil), candidates...)
	}
	r.mu.Lock()
	r.classes = copied
	r.mu.Unlock()
}

type candidate struct {
	model    domain.Model
	provider ports.Provider
}

// candidates applies DD-004's filters to the class, keeping the declared order. The offline
// filter is the catalog's: offline, a remote provider is not callable and its models are down.
func (r *Router) candidates(ctx context.Context, call Call) ([]candidate, error) {
	r.mu.RLock()
	ids, ok := r.classes[call.Class]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", domain.ErrUnknownClass, call.Class)
	}
	listed, err := r.catalog.List(ctx, false)
	if err != nil {
		return nil, err
	}
	models := make(map[string]domain.Model, len(listed))
	for _, m := range listed {
		models[m.ID] = m
	}
	need := estimateTokens(call.Request)
	out := make([]candidate, 0, len(ids))
	for _, id := range ids {
		m, ok := models[id]
		if !ok || m.Health == domain.HealthDown {
			continue
		}
		p, ok := r.catalog.Provider(m.Provider)
		if !ok || !capable(m.Caps, call.Request, need) {
			continue
		}
		out = append(out, candidate{model: m, provider: p})
	}
	return out, nil
}

// capable is the capability filter: tools, structured output, reasoning and the window.
func capable(caps domain.Capabilities, req domain.Request, need int64) bool {
	switch {
	case len(req.Tools) > 0 && !caps.Tools:
		return false
	case req.ResponseSchema != nil && !caps.JSONSchema:
		return false
	case req.Reasoning != "" && req.Reasoning != "off" && !caps.Reasoning:
		return false
	case caps.ContextWindow > 0 && need > caps.ContextWindow:
		return false
	}
	return true
}

// estimateTokens is a deliberately rough count — four characters a token — for the window
// filter only; what a call consumed is the provider's own count.
func estimateTokens(req domain.Request) int64 {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Text)
		for _, tc := range m.ToolCalls {
			n += len(tc.Input)
		}
	}
	return int64(n/4) + req.MaxOutputTokens
}

// redact returns a copy of req with every piece of content the model will read redacted
// (REQ-SEC-001: all content), the caller's request untouched.
func (r *Router) redact(req domain.Request) domain.Request {
	out := req
	out.System = r.cfg.Redact(req.System)
	if req.Tools != nil {
		out.Tools = make([]domain.ToolSpec, len(req.Tools))
		for i, t := range req.Tools {
			t.Description = r.cfg.Redact(t.Description)
			t.InputSchema, _ = r.redactValue(t.InputSchema).(map[string]any)
			out.Tools[i] = t
		}
	}
	if req.ResponseSchema != nil {
		out.ResponseSchema, _ = r.redactValue(req.ResponseSchema).(map[string]any)
	}
	out.Messages = make([]domain.Message, len(req.Messages))
	for i, m := range req.Messages {
		m.Text = r.cfg.Redact(m.Text)
		if len(m.ToolCalls) > 0 {
			calls := make([]domain.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				tc.Input = r.cfg.Redact(tc.Input)
				calls[j] = tc
			}
			m.ToolCalls = calls
		}
		out.Messages[i] = m
	}
	return out
}

// redactValue returns a copy of a JSON value with every string in it redacted: a schema's
// descriptions, defaults, enums and examples are content the model reads too.
func (r *Router) redactValue(v any) any {
	switch v := v.(type) {
	case string:
		return r.cfg.Redact(v)
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = r.redactValue(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = r.redactValue(e)
		}
		return out
	default:
		return v
	}
}

// Stream routes one call. A class that is unknown, or has no candidate left after the
// filters, is an error here, before anything is sent. The fallback happens while the stream
// is read: until a candidate delivers its first event, a retryable failure moves to the next
// one; after it, the failure ends the call. When every candidate fails the stream ends with
// ErrNoCandidate.
func (r *Router) Stream(ctx context.Context, call Call) (iter.Seq2[domain.Event, error], error) {
	cands, err := r.candidates(ctx, call)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: class %q", domain.ErrNoCandidate, call.Class)
	}
	req := r.redact(call.Request)
	ctx = domain.WithThread(ctx, call.ThreadID)
	return func(yield func(domain.Event, error) bool) {
		var errs []error
		for _, c := range cands {
			finished, err := r.attempt(ctx, call, c, req, yield)
			if finished {
				return
			}
			errs = append(errs, err)
		}
		yield(domain.Event{}, fmt.Errorf("%w: class %q: %w", domain.ErrNoCandidate, call.Class, errors.Join(errs...)))
	}, nil
}

type item struct {
	ev  domain.Event
	err error
}

// attempt makes the call on one candidate. finished is true when the call is over — it
// succeeded, or it failed in a way the next candidate cannot fix — and false with the reason
// when the next candidate should be tried.
func (r *Router) attempt(ctx context.Context, call Call, c candidate, req domain.Request, yield func(domain.Event, error) bool) (finished bool, _ error) {
	start := r.now()
	actx, cancel := context.WithCancel(ctx)
	defer cancel()

	req.Model = c.model.Name()
	rec := domain.UsageRecord{
		ThreadID: call.ThreadID, TurnID: call.TurnID, ModelID: c.model.ID, Provider: c.model.Provider,
	}
	// The timer runs before Stream is called: the adapters send the request inside it, and a
	// server that never sends its headers — an Ollama loading a model — must time out too.
	timeout := r.cfg.FirstTokenRemote
	if c.provider.Local() {
		timeout = r.cfg.FirstTokenLocal
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// The pump opens the stream and forwards it. It is not waited for: it ends once the
	// adapter honours actx, which ports.Provider requires.
	events := make(chan item)
	go func() {
		defer close(events)
		seq, err := c.provider.Stream(actx, req)
		if err != nil {
			select {
			case events <- item{err: err}:
			case <-actx.Done():
			}
			return
		}
		for ev, err := range seq {
			select {
			case events <- item{ev, err}:
			case <-actx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var first item
	var open bool
	select {
	case first, open = <-events:
	case <-timer.C:
		cancel()
		err := &domain.ProviderError{Provider: c.model.Provider, Err: fmt.Errorf("no first token within %v", timeout)}
		rec.Status, rec.Error = domain.UsageTimeout, err.Error()
		r.record(ctx, rec)
		return false, err
	case <-ctx.Done():
		first = item{err: ctx.Err()}
		open = true
	}
	if !open {
		first.err = &domain.ProviderError{Provider: c.model.Provider, Err: errors.New("the stream ended without an event")}
	}
	if first.err != nil {
		return r.failedBeforeFirst(ctx, rec, first.err, yield)
	}

	ms := r.now().Sub(start).Milliseconds()
	rec.FirstTokenMS = &ms
	var usage domain.Usage
	finish := func(err error) {
		rec.InTokens, rec.OutTokens = usage.InputTokens, usage.OutputTokens
		rec.CostMicroUSD = c.model.Cost(usage.InputTokens, usage.OutputTokens)
		rec.Status = domain.UsageOK
		if err != nil {
			rec.Status, rec.Error = domain.UsageError, err.Error()
		}
		r.record(ctx, rec)
	}
	for it := first; ; {
		if it.err != nil {
			finish(it.err)
			yield(domain.Event{}, it.err)
			return true, nil
		}
		if it.ev.Kind == domain.EventUsage {
			usage = it.ev.Usage
		}
		if !yield(it.ev, nil) {
			finish(errors.New("the caller stopped reading"))
			return true, nil
		}
		next, ok := <-events
		if !ok {
			finish(nil)
			return true, nil
		}
		it = next
	}
}

// failedBeforeFirst handles a failure before any event reached the caller: a request the
// adapter cannot carry is skipped unrecorded; a retryable provider failure is recorded and
// moves on; anything else is recorded and ends the call.
func (r *Router) failedBeforeFirst(ctx context.Context, rec domain.UsageRecord, err error, yield func(domain.Event, error) bool) (bool, error) {
	if errors.Is(err, domain.ErrUnsupported) {
		return false, err
	}
	rec.Status, rec.Error = domain.UsageError, err.Error()
	var pe *domain.ProviderError
	retry := errors.As(err, &pe) && pe.Retryable() && ctx.Err() == nil
	if retry && pe.Status == 429 {
		rec.Status = domain.UsageRateLimited
	}
	r.record(ctx, rec)
	if retry {
		return false, err
	}
	yield(domain.Event{}, err)
	return true, nil
}

// record writes one usage row. It uses a context the caller's cancellation cannot stop, since
// a cancelled call is still a call that happened.
func (r *Router) record(ctx context.Context, rec domain.UsageRecord) {
	rec.CreatedAt = r.now().UnixMilli()
	if err := r.cfg.Usage.Record(context.WithoutCancel(ctx), rec); err != nil {
		r.cfg.Logger.Warn("a model call could not be recorded in usage",
			slog.String("thread", rec.ThreadID), slog.String("turn", rec.TurnID),
			slog.String("model", rec.ModelID), slog.String("status", rec.Status), slog.Any("error", err))
	}
}
