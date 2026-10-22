package llmgw

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// The `[router] policy` values (REQ-RTR-003). They order a class's candidates after the hard
// constraints and the capability filter; a pinned model is never reordered.
const (
	// PolicyLocalFirst puts local candidates before remote ones, each group in declared order.
	PolicyLocalFirst = "local-first"
	// PolicyCost orders by price per million tokens, input and output added. A local model
	// costs nothing; a remote one whose provider publishes no price comes after every priced
	// one, since an unknown cost is not a free one.
	PolicyCost = "cost"
	// PolicyQuality keeps the declared order: a class lists its candidates best first, and the
	// catalog has no measure of quality to sort by.
	PolicyQuality = "quality"
)

// The breaker defaults of REQ-RTR-002.
const (
	DefaultBreakerFailures = 3
	DefaultBreakerCooldown = 60 * time.Second
)

// Policy is the `[router]` table's routing keys.
type Policy struct {
	// Order is PolicyLocalFirst, PolicyCost or PolicyQuality; empty keeps the declared order.
	Order string
	// BreakerFailures and BreakerCooldown are REQ-RTR-002's; zero keeps the defaults.
	BreakerFailures int
	BreakerCooldown time.Duration
}

// Why a candidate was kept or dropped, as model.route answers it.
const (
	ReasonEligible       = "eligible"
	ReasonNotInCatalog   = "not_in_catalog"
	ReasonDown           = "down"
	ReasonNoTools        = "no_tools"
	ReasonNoVision       = "no_vision"
	ReasonNoJSONSchema   = "no_json_schema"
	ReasonNoReasoning    = "no_reasoning"
	ReasonWindowTooSmall = "window_too_small"
	ReasonBreakerOpen    = "breaker_open"
)

// Needs is a request's shape as the capability filter sees it.
type Needs struct {
	Tools, Vision, JSONSchema, Reasoning bool
	// Tokens is the estimated prompt plus the response reserve; 0 needs no particular window.
	Tokens int64
}

// NeedsOf is what a request needs from a model.
func NeedsOf(req domain.Request) Needs {
	return Needs{
		Tools:      len(req.Tools) > 0,
		Vision:     req.Vision,
		JSONSchema: req.ResponseSchema != nil,
		Reasoning:  req.Reasoning != "" && req.Reasoning != "off",
		Tokens:     estimateTokens(req),
	}
}

// missing is the first capability caps lacks for needs, or "" when it has them all.
func (n Needs) missing(caps domain.Capabilities) string {
	switch {
	case n.Tools && !caps.Tools:
		return ReasonNoTools
	case n.Vision && !caps.Vision:
		return ReasonNoVision
	case n.JSONSchema && !caps.JSONSchema:
		return ReasonNoJSONSchema
	case n.Reasoning && !caps.Reasoning:
		return ReasonNoReasoning
	case caps.ContextWindow > 0 && n.Tokens > caps.ContextWindow:
		return ReasonWindowTooSmall
	}
	return ""
}

// RouteQuery is model.route's question: a class, or one pinned model, and a request shape.
type RouteQuery struct {
	Class string
	Model string
	Needs Needs
}

// RouteCandidate is one candidate and why it was kept or dropped.
type RouteCandidate struct {
	ModelID  string
	Provider string
	Local    bool
	Kept     bool
	Reason   string
	// Health is the catalog's, "" for a candidate not in it.
	Health                                          domain.Health
	PriceInMicroUSDPerMTok, PriceOutMicroUSDPerMTok int64
	ContextWindow                                   int64
	// DeclaredIndex is the candidate's position in the class as configured.
	DeclaredIndex int
}

// Route is the candidates in the order a call would try them, the kept ones first, then the
// dropped ones in declared order.
type Route struct {
	Policy     string
	Candidates []RouteCandidate
}

// SetPolicy replaces the routing policy and the breaker settings, at start and on reload. The
// breakers keep their state: a reload does not forgive a provider that is failing.
func (r *Router) SetPolicy(p Policy) {
	if p.BreakerFailures <= 0 {
		p.BreakerFailures = DefaultBreakerFailures
	}
	if p.BreakerCooldown <= 0 {
		p.BreakerCooldown = DefaultBreakerCooldown
	}
	r.mu.Lock()
	r.policy = p
	r.mu.Unlock()
	r.breakers.configure(p.BreakerFailures, p.BreakerCooldown)
}

// PolicyOrder is the policy in force.
func (r *Router) PolicyOrder() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy.Order
}

// Route is model.route (REQ-RTR-003): the candidates of a class, or the one pinned model, each
// kept or dropped with its reason, in the order a call with that shape would try them now. It
// sends nothing and takes no breaker's probe.
func (r *Router) Route(ctx context.Context, q RouteQuery) (Route, error) {
	policy := r.PolicyOrder()
	planned, err := r.plan(ctx, q)
	if err != nil {
		return Route{}, err
	}
	out := Route{Policy: policy, Candidates: make([]RouteCandidate, 0, len(planned))}
	for _, p := range planned {
		out.Candidates = append(out.Candidates, p.RouteCandidate)
	}
	return out, nil
}

// planned is a route candidate with what a call needs to make it.
type planned struct {
	RouteCandidate
	candidate
}

// plan applies DD-004's filters and REQ-RTR-002's breakers to the class and orders what is left
// by the policy: the kept candidates in call order, then the dropped ones in declared order.
func (r *Router) plan(ctx context.Context, q RouteQuery) ([]planned, error) {
	ids, err := r.candidateIDs(q.Class, q.Model)
	if err != nil {
		return nil, err
	}
	listed, err := r.catalog.List(ctx, false)
	if err != nil {
		return nil, err
	}
	models := make(map[string]domain.Model, len(listed))
	for _, m := range listed {
		models[m.ID] = m
	}
	out := make([]planned, 0, len(ids))
	for i, id := range ids {
		pc := planned{RouteCandidate: RouteCandidate{ModelID: id, DeclaredIndex: i, Reason: ReasonEligible}}
		m, ok := models[id]
		if !ok {
			pc.Reason = ReasonNotInCatalog
			out = append(out, pc)
			continue
		}
		pc.Provider, pc.Local, pc.Health = m.Provider, m.Local, m.Health
		pc.PriceInMicroUSDPerMTok, pc.PriceOutMicroUSDPerMTok = m.PriceInMicroUSDPerMTok, m.PriceOutMicroUSDPerMTok
		pc.ContextWindow = m.Caps.ContextWindow
		p, callable := r.catalog.Provider(m.Provider)
		if callable {
			pc.Local = p.Local()
		}
		switch {
		case m.Health == domain.HealthDown || !callable:
			pc.Reason = ReasonDown
		case q.Needs.missing(m.Caps) != "":
			pc.Reason = q.Needs.missing(m.Caps)
		case r.breakers.open(m.Provider):
			pc.Reason = ReasonBreakerOpen
		default:
			pc.Kept = true
			pc.candidate = candidate{model: m, provider: p}
		}
		out = append(out, pc)
	}
	kept := slices.DeleteFunc(slices.Clone(out), func(p planned) bool { return !p.Kept })
	if q.Model == "" {
		policy := r.PolicyOrder()
		slices.SortStableFunc(kept, func(a, b planned) int { return compareBy(policy, a.RouteCandidate, b.RouteCandidate) })
	}
	return append(kept, slices.DeleteFunc(out, func(p planned) bool { return p.Kept })...), nil
}

// compareBy orders two candidates under a policy; 0 keeps their declared order.
func compareBy(policy string, a, b RouteCandidate) int {
	switch policy {
	case PolicyLocalFirst:
		return rankBool(a.Local) - rankBool(b.Local)
	case PolicyCost:
		ca, cb := costKey(a), costKey(b)
		switch {
		case ca < cb:
			return -1
		case ca > cb:
			return 1
		}
	}
	return 0
}

func rankBool(local bool) int {
	if local {
		return 0
	}
	return 1
}

// unpriced is the cost key of a remote model without a published price: after every priced one.
const unpriced = int64(1) << 62

func costKey(c RouteCandidate) int64 {
	switch {
	case c.Local:
		return 0
	case c.PriceInMicroUSDPerMTok == 0 && c.PriceOutMicroUSDPerMTok == 0:
		return unpriced
	}
	return c.PriceInMicroUSDPerMTok + c.PriceOutMicroUSDPerMTok
}

// breakers are REQ-RTR-002's circuit breakers, one per provider, kept in memory.
type breakers struct {
	now func() time.Time

	mu       sync.Mutex
	failures int
	cooldown time.Duration
	state    map[string]*breaker
}

// breaker is one provider's: closed while failures is under the threshold; open until
// openUntil; then one probe at a time may go through.
type breaker struct {
	failures  int
	open      bool
	openUntil time.Time
	probing   bool
}

// outcome is what one attempt says about its provider.
type outcome int

const (
	// outcomeNone says nothing: the request's own fault, an unsupported request, a cancel.
	outcomeNone outcome = iota
	outcomeSuccess
	outcomeFailure
)

func newBreakers(now func() time.Time) *breakers {
	return &breakers{
		now: now, failures: DefaultBreakerFailures, cooldown: DefaultBreakerCooldown,
		state: map[string]*breaker{},
	}
}

func (b *breakers) configure(failures int, cooldown time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures, b.cooldown = failures, cooldown
}

// open reports whether a call to provider would be refused now. It takes no probe.
func (b *breakers) open(provider string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state[provider]
	return s != nil && s.open && (b.now().Before(s.openUntil) || s.probing)
}

// acquire asks to call provider. A closed breaker always allows it; an open one refuses until
// its cooldown has passed, then allows one probe at a time.
func (b *breakers) acquire(provider string) (ok, probe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state[provider]
	switch {
	case s == nil || !s.open:
		return true, false
	case b.now().Before(s.openUntil) || s.probing:
		return false, false
	}
	s.probing = true
	return true, true
}

// transition is what one report did to a breaker, for the log.
type transition int

const (
	unchanged transition = iota
	opened
	closed
)

// report records an attempt's outcome. A success closes the breaker; a failure counts towards
// opening it, and a failed probe opens it for another cooldown. While the breaker is open only
// the probe's outcome counts: a call that started before it opened says nothing about whether
// the provider has recovered, so neither its success nor its failure moves the breaker.
func (b *breakers) report(provider string, probe bool, o outcome) transition {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state[provider]
	if s == nil {
		s = &breaker{}
		b.state[provider] = s
	}
	if s.open && !probe {
		return unchanged
	}
	if probe {
		s.probing = false
	}
	switch o {
	case outcomeSuccess:
		was := s.open
		*s = breaker{}
		if was {
			return closed
		}
	case outcomeFailure:
		s.failures++
		if probe || s.failures >= b.failures {
			was := s.open
			s.open, s.openUntil = true, b.now().Add(b.cooldown)
			if !was {
				return opened
			}
		}
	}
	return unchanged
}
