package llmgw

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// routeIDs are the kept candidates of a route, in the order the router would try them.
func routeIDs(rt Route) []string {
	var out []string
	for _, c := range rt.Candidates {
		if c.Kept {
			out = append(out, c.ModelID)
		}
	}
	return out
}

func reasonOf(rt Route, id string) string {
	for _, c := range rt.Candidates {
		if c.ModelID == id {
			return c.Reason
		}
	}
	return "(absent)"
}

// TestACandidateWithoutToolsIsDropped_REQ_RTR_001: before anything is sent, a candidate that lacks
// a capability the request needs — tools, vision, structured output, reasoning or a window large
// enough — is dropped with the reason, and the call goes to the first one that has them all.
func TestACandidateWithoutToolsIsDropped_REQ_RTR_001(t *testing.T) {
	t.Parallel()
	p := &streamProvider{id: "p", local: true, scripts: map[string]script{"full": {events: answer}}}
	noTools := model("p", "no-tools")
	noTools.Caps.Tools = false
	noVision := model("p", "no-vision")
	small := model("p", "small")
	small.Caps.ContextWindow, small.Caps.Vision = 100, true
	full := model("p", "full")
	full.Caps.Vision = true
	r := newRig(t, false,
		map[string][]string{"code": {"p/no-tools", "p/no-vision", "p/small", "p/full"}},
		[]*streamProvider{p}, []domain.Model{noTools, noVision, small, full})

	call := hello("code")
	call.Request.Tools = []domain.ToolSpec{{Name: "read_file"}}
	call.Request.Vision = true
	call.Request.MaxOutputTokens = 500
	text, err := collect(t, r.router, call)
	if err != nil || text != "hello" {
		t.Fatalf("text %q, err %v", text, err)
	}
	if p.called() != 1 || p.requests[0].Model != "full" {
		t.Fatalf("calls %d, first to %q; want only p/full", p.called(), p.requests[0].Model)
	}

	rt, err := r.router.Route(context.Background(), RouteQuery{Class: "code", Needs: NeedsOf(call.Request)})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	for id, want := range map[string]string{
		"p/no-tools": ReasonNoTools, "p/no-vision": ReasonNoVision, "p/small": ReasonWindowTooSmall, "p/full": ReasonEligible,
	} {
		if got := reasonOf(rt, id); got != want {
			t.Errorf("%s: reason %q, want %q", id, got, want)
		}
	}
	if got := routeIDs(rt); !slices.Equal(got, []string{"p/full"}) {
		t.Errorf("kept %v", got)
	}
}

// TestRouterPoliciesOrderTheCandidates_REQ_RTR_003: after the hard constraints and the capability
// filter, `local-first` puts local candidates first, `cost` orders by price — an unpriced remote
// model after every priced one — and `quality` keeps the declared order; ties keep it too.
func TestRouterPoliciesOrderTheCandidates_REQ_RTR_003(t *testing.T) {
	t.Parallel()
	remote := &streamProvider{id: "or"}
	local := &streamProvider{id: "ol", local: true}
	priced := func(name string, in, out int64) domain.Model {
		m := model("or", name)
		m.PriceInMicroUSDPerMTok, m.PriceOutMicroUSDPerMTok = in, out
		return m
	}
	models := []domain.Model{
		priced("dear", 3_000_000, 15_000_000), priced("unpriced", 0, 0), model("ol", "big"),
		priced("cheap", 100_000, 400_000), model("ol", "small"),
	}
	classes := map[string][]string{"code": {"or/dear", "or/unpriced", "ol/big", "or/cheap", "ol/small"}}
	for _, tc := range []struct {
		policy string
		want   []string
	}{
		{PolicyLocalFirst, []string{"ol/big", "ol/small", "or/dear", "or/unpriced", "or/cheap"}},
		{PolicyCost, []string{"ol/big", "ol/small", "or/cheap", "or/dear", "or/unpriced"}},
		{PolicyQuality, []string{"or/dear", "or/unpriced", "ol/big", "or/cheap", "ol/small"}},
	} {
		r := newRig(t, false, classes, []*streamProvider{remote, local}, models)
		r.router.SetPolicy(Policy{Order: tc.policy})
		rt, err := r.router.Route(context.Background(), RouteQuery{Class: "code"})
		if err != nil {
			t.Fatalf("%s: Route: %v", tc.policy, err)
		}
		if got := routeIDs(rt); !slices.Equal(got, tc.want) {
			t.Errorf("%s: order %v, want %v", tc.policy, got, tc.want)
		}
		if rt.Policy != tc.policy {
			t.Errorf("%s: route names policy %q", tc.policy, rt.Policy)
		}
	}

	// Dropped candidates follow in declared order, whatever the policy would make of them.
	r0 := newRig(t, false, classes, []*streamProvider{remote, local}, models)
	r0.router.SetPolicy(Policy{Order: PolicyLocalFirst})
	none, err := r0.router.Route(context.Background(), RouteQuery{Class: "code", Needs: Needs{Vision: true}})
	if err != nil {
		t.Fatal(err)
	}
	dropped := make([]string, 0, len(none.Candidates))
	for _, c := range none.Candidates {
		dropped = append(dropped, c.ModelID)
	}
	if want := classes["code"]; !slices.Equal(dropped, want) {
		t.Errorf("dropped in %v, want declared order %v", dropped, want)
	}

	// The order the route reports is the order a call walks.
	r := newRig(t, false, classes, []*streamProvider{remote, local}, models)
	r.router.SetPolicy(Policy{Order: PolicyCost})
	for _, p := range []*streamProvider{remote, local} {
		p.scripts = map[string]script{}
	}
	local.scripts["big"] = script{startErr: status(503)}
	local.scripts["small"] = script{startErr: status(503)}
	remote.scripts["cheap"] = script{events: answer}
	if text, err := collect(t, r.router, hello("code")); err != nil || text != "hello" {
		t.Fatalf("text %q, err %v", text, err)
	}
	if remote.called() != 1 || remote.requests[0].Model != "cheap" {
		t.Errorf("the remote calls were %d, first to %v; want or/cheap after both local ones", remote.called(), remote.requests)
	}
}

// fakeClock is a router's clock the test moves.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestABreakerOpensAndProbes_REQ_RTR_002: a provider that fails breaker_failures calls in a row is
// skipped for breaker_cooldown without being called; after it, one call probes it; a failed probe
// opens it again, and a successful one closes it. A refusal that is the request's fault (a 400)
// does not count.
func TestABreakerOpensAndProbes_REQ_RTR_002(t *testing.T) {
	t.Parallel()
	flaky := &streamProvider{id: "flaky", local: true, scripts: map[string]script{"m": {startErr: status(503)}}}
	backup := &streamProvider{id: "backup", local: true, scripts: map[string]script{"m": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"flaky/m", "backup/m"}},
		[]*streamProvider{flaky, backup}, []domain.Model{model("flaky", "m"), model("backup", "m")})
	clock := &fakeClock{t: time.UnixMilli(1_760_000_000_000)}
	r.router.now = clock.now
	r.router.SetPolicy(Policy{Order: PolicyQuality, BreakerFailures: 2, BreakerCooldown: time.Minute})

	call := func() {
		t.Helper()
		if text, err := collect(t, r.router, hello("code")); err != nil || text != "hello" {
			t.Fatalf("text %q, err %v", text, err)
		}
	}
	breakerReason := func() string {
		t.Helper()
		rt, err := r.router.Route(context.Background(), RouteQuery{Class: "code"})
		if err != nil {
			t.Fatal(err)
		}
		return reasonOf(rt, "flaky/m")
	}

	// A 400 is the request's fault, not the provider's: it ends the call and counts nothing.
	flaky.scripts["m"] = script{startErr: status(400)}
	if _, err := collect(t, r.router, hello("code")); err == nil {
		t.Fatal("a 400 did not end the call")
	}
	flaky.scripts["m"] = script{startErr: status(503)}
	call()
	if got := breakerReason(); got != ReasonEligible {
		t.Fatalf("after a 400 and one failure the breaker reads %q", got)
	}
	call()
	if got := breakerReason(); got != ReasonBreakerOpen {
		t.Fatalf("after two failures in a row the reason is %q, want %q", got, ReasonBreakerOpen)
	}
	before := flaky.called()
	call()
	clock.advance(59 * time.Second)
	call()
	if flaky.called() != before {
		t.Fatalf("an open breaker's provider was called %d times", flaky.called()-before)
	}

	// Cooled down: one call probes it, and the probe fails, so it opens again.
	clock.advance(2 * time.Second)
	if got := breakerReason(); got != ReasonEligible {
		t.Fatalf("a cooled-down breaker reads %q; it should let one probe through", got)
	}
	call()
	if flaky.called() != before+1 {
		t.Fatalf("the probe made %d calls, want 1", flaky.called()-before)
	}
	call()
	if flaky.called() != before+1 || breakerReason() != ReasonBreakerOpen {
		t.Fatalf("a failed probe did not open the breaker again")
	}

	// The next probe succeeds and closes it.
	clock.advance(61 * time.Second)
	flaky.scripts["m"] = script{events: answer}
	call()
	if flaky.called() != before+2 || breakerReason() != ReasonEligible {
		t.Fatalf("a successful probe did not close the breaker")
	}
	call()
	if flaky.called() != before+3 {
		t.Errorf("a closed breaker's provider was not called")
	}

	// Every candidate behind an open breaker: the call fails before sending anything.
	flaky.scripts["m"] = script{startErr: status(503)}
	backup.scripts["m"] = script{startErr: status(503)}
	for range 2 {
		_, _ = collect(t, r.router, hello("code"))
	}
	sent := flaky.called() + backup.called()
	if _, err := r.router.Stream(context.Background(), hello("code")); !errors.Is(err, domain.ErrNoCandidate) {
		t.Errorf("with every breaker open Stream returned %v, want ErrNoCandidate", err)
	}
	if flaky.called()+backup.called() != sent {
		t.Errorf("a provider behind an open breaker was called")
	}
}

// probeRig is a provider whose breaker opens at one failure, and a backup that always answers.
func probeRig(t *testing.T) (rig, *streamProvider, *fakeClock) {
	t.Helper()
	flaky := &streamProvider{id: "flaky", local: true, scripts: map[string]script{"m": {startErr: status(503)}}}
	backup := &streamProvider{id: "backup", local: true, scripts: map[string]script{"m": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"flaky/m", "backup/m"}},
		[]*streamProvider{flaky, backup}, []domain.Model{model("flaky", "m"), model("backup", "m")})
	clock := &fakeClock{t: time.UnixMilli(1_760_000_000_000)}
	r.router.now = clock.now
	r.router.SetPolicy(Policy{Order: PolicyQuality, BreakerFailures: 1, BreakerCooldown: time.Minute})
	// A slow probe must not time out: these tests hold calls in flight on purpose.
	r.router.cfg.FirstTokenLocal = 5 * time.Second
	if text, err := collect(t, r.router, hello("code")); err != nil || text != "hello" {
		t.Fatalf("text %q, err %v", text, err)
	}
	return r, flaky, clock
}

func flakyReason(t *testing.T, r rig) string {
	t.Helper()
	rt, err := r.router.Route(context.Background(), RouteQuery{Class: "code"})
	if err != nil {
		t.Fatal(err)
	}
	return reasonOf(rt, "flaky/m")
}

// TestOneProbeAtATimeUntilItsFirstEvent_REQ_RTR_002: while a probe is in flight the provider reads
// breaker_open and a second call skips it; the probe's first event closes the breaker at once,
// before its answer is over.
func TestOneProbeAtATimeUntilItsFirstEvent_REQ_RTR_002(t *testing.T) {
	t.Parallel()
	r, flaky, clock := probeRig(t)
	if got := flakyReason(t, r); got != ReasonBreakerOpen {
		t.Fatalf("after one failure the breaker reads %q", got)
	}
	clock.advance(61 * time.Second)
	// The probe takes its time to answer, then keeps generating until it is cancelled.
	flaky.scripts["m"] = script{delay: 400 * time.Millisecond, events: answer[:1], hangAfter: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := r.router.Stream(ctx, hello("code"))
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	go func() {
		for _, err := range stream {
			if err == nil {
				select {
				case <-first:
				default:
					close(first)
				}
			}
		}
	}()
	for flaky.called() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := flakyReason(t, r); got != ReasonBreakerOpen {
		t.Errorf("with a probe in flight the breaker reads %q", got)
	}
	if text, err := collect(t, r.router, hello("code")); err != nil || text != "hello" || flaky.called() != 2 {
		t.Errorf("a second call during the probe: %q, %v, %d calls to the probed provider", text, err, flaky.called())
	}
	<-first
	if got := flakyReason(t, r); got != ReasonEligible {
		t.Errorf("after the probe's first event, with its answer still streaming, the breaker reads %q", got)
	}
	cancel()
}

// TestALateSuccessDoesNotCloseAnOpenBreaker_REQ_RTR_002: a call that started before the breaker
// opened and succeeds after it says nothing about recovery; only a probe closes the breaker.
func TestALateSuccessDoesNotCloseAnOpenBreaker_REQ_RTR_002(t *testing.T) {
	t.Parallel()
	flaky := &streamProvider{id: "flaky", local: true, scripts: map[string]script{
		"slow": {delay: 400 * time.Millisecond, events: answer},
		"bad":  {startErr: status(503)},
	}}
	backup := &streamProvider{id: "backup", local: true, scripts: map[string]script{"m": {events: answer}}}
	r := newRig(t, false, map[string][]string{"code": {"flaky/slow"}, "plan": {"flaky/bad", "backup/m"}},
		[]*streamProvider{flaky, backup}, []domain.Model{model("flaky", "slow"), model("flaky", "bad"), model("backup", "m")})
	r.router.SetPolicy(Policy{BreakerFailures: 1, BreakerCooldown: time.Hour})
	r.router.cfg.FirstTokenLocal = 5 * time.Second

	done := make(chan error, 1)
	go func() {
		_, err := collect(t, r.router, hello("code"))
		done <- err
	}()
	for flaky.called() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := collect(t, r.router, hello("plan")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the slow call: %v", err)
	}
	rt, err := r.router.Route(context.Background(), RouteQuery{Class: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reasonOf(rt, "flaky/bad"); got != ReasonBreakerOpen {
		t.Errorf("a success that started before the breaker opened closed it: %q", got)
	}
}
