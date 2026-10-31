package llmgw

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// concurrently makes n calls of the class at once and returns their errors.
func concurrently(t *testing.T, r *Router, class string, n int) []error {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { _, errs[i] = collect(t, r, hello(class)) })
	}
	wg.Wait()
	return errs
}

// TestLocalCallsQueuePerProvider_REQ_ORC_005: calls competing for a local provider are sent one
// at a time unless its max_parallel says otherwise; a remote provider is not queued by default.
func TestLocalCallsQueuePerProvider_REQ_ORC_005(t *testing.T) {
	t.Parallel()

	// 30 ms keeps three calls overlapping and leaves the remote's 150 ms first-token timeout a
	// margin the Windows runner under -race needs; 60 ms ran out of it there.
	slow := script{delay: 30 * time.Millisecond, events: answer}
	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{"m": slow}}
	remote := &streamProvider{id: "remote", scripts: map[string]script{"m": slow}}
	wide := &streamProvider{id: "lmstudio", local: true, scripts: map[string]script{"m": slow}}
	r := newRig(t, false, map[string][]string{"local": {"ollama/m"}, "remote": {"remote/m"}, "wide": {"lmstudio/m"}},
		[]*streamProvider{local, remote, wide},
		[]domain.Model{model("ollama", "m"), model("remote", "m"), model("lmstudio", "m")})
	r.router.SetParallel(map[string]int{"lmstudio": 2})

	for _, c := range []struct {
		class string
		p     *streamProvider
		want  int
	}{{"local", local, 1}, {"remote", remote, 3}, {"wide", wide, 2}} {
		for i, err := range concurrently(t, r.router, c.class, 3) {
			if err != nil {
				t.Fatalf("%s call %d: %v", c.class, i, err)
			}
		}
		if got := c.p.mostAtOnce(); got != c.want {
			t.Errorf("%s: %d calls were sent at once, want %d", c.class, got, c.want)
		}
	}
}

// TestQueueTimeIsNotFirstTokenTime_REQ_ORC_005: a call waiting behind another for its provider
// is not timed out for it: the first-token clock starts when the call is sent, and the recorded
// first-token time leaves the queue out.
func TestQueueTimeIsNotFirstTokenTime_REQ_ORC_005(t *testing.T) {
	t.Parallel()

	// Each answer takes 200 ms of the local provider's 300 ms first-token timeout, so two
	// in a row overrun it if the queue counts.
	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{
		"m": {delay: 200 * time.Millisecond, events: answer},
	}}
	r := newRig(t, false, map[string][]string{"code": {"ollama/m"}},
		[]*streamProvider{local}, []domain.Model{model("ollama", "m")})

	for i, err := range concurrently(t, r.router, "code", 2) {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	recs := r.usage.all()
	if len(recs) != 2 {
		t.Fatalf("%d usage rows, want 2", len(recs))
	}
	for _, rec := range recs {
		if rec.Status != domain.UsageOK {
			t.Fatalf("a queued call was recorded %s: %s", rec.Status, rec.Error)
		}
		if rec.FirstTokenMS == nil || *rec.FirstTokenMS >= 300 {
			t.Fatalf("first_token_ms %v includes the queue", rec.FirstTokenMS)
		}
	}
}

// TestACallCancelledInTheQueueIsNotSent_REQ_ORC_005: a call given up while it waits for its
// provider never reaches it and frees its place.
func TestACallCancelledInTheQueueIsNotSent_REQ_ORC_005(t *testing.T) {
	t.Parallel()

	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{
		"busy": {events: answer[:1], hangAfter: true}, "m": {events: answer},
	}}
	r := newRig(t, false, map[string][]string{"busy": {"ollama/busy"}, "code": {"ollama/m"}},
		[]*streamProvider{local}, []domain.Model{model("ollama", "busy"), model("ollama", "m")})

	busyCtx, stopBusy := context.WithCancel(t.Context())
	busy, err := r.router.Stream(busyCtx, hello("busy"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	go func() {
		for range busy {
			select {
			case <-started:
			default:
				close(started)
			}
		}
	}()
	<-started

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	stream, err := r.router.Stream(ctx, hello("code"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []domain.EventKind
	var ended error
	for ev, err := range stream {
		if err != nil {
			ended = err
			break
		}
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 1 || kinds[0] != domain.EventQueued || ended == nil {
		t.Fatalf("a call given up in the queue got %v then %v, want only queued then an error", kinds, ended)
	}
	if n := local.called(); n != 1 {
		t.Fatalf("the provider got %d calls, want only the busy one", n)
	}
	stopBusy()
	if _, err := collect(t, r.router, hello("code")); err != nil {
		t.Fatalf("after the busy call ended: %v", err)
	}
}

// TestAReloadKeepsTheLocalDefault_REQ_ORC_005: a config.reload that names no max_parallel
// leaves a local provider at one call at a time, waiters included.
func TestAReloadKeepsTheLocalDefault_REQ_ORC_005(t *testing.T) {
	t.Parallel()

	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{
		"m": {delay: 60 * time.Millisecond, events: answer},
	}}
	r := newRig(t, false, map[string][]string{"code": {"ollama/m"}},
		[]*streamProvider{local}, []domain.Model{model("ollama", "m")})

	done := make(chan []error)
	go func() { done <- concurrently(t, r.router, "code", 3) }()
	time.Sleep(20 * time.Millisecond) // one call sent, two waiting
	r.router.SetParallel(map[string]int{})
	for i, err := range <-done {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := local.mostAtOnce(); got != 1 {
		t.Fatalf("%d calls reached the local provider at once after a reload, want 1", got)
	}
}

// TestOnlyAWaitingCallSaysItIsQueued_REQ_ORC_005: a call with a free place starts with the
// provider's own events; one behind another call says queued first.
func TestOnlyAWaitingCallSaysItIsQueued_REQ_ORC_005(t *testing.T) {
	t.Parallel()

	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{
		"m": {delay: 60 * time.Millisecond, events: answer},
	}}
	r := newRig(t, false, map[string][]string{"code": {"ollama/m"}},
		[]*streamProvider{local}, []domain.Model{model("ollama", "m")})

	first := func() []domain.EventKind {
		stream, err := r.router.Stream(t.Context(), hello("code"))
		if err != nil {
			t.Error(err)
			return nil
		}
		var kinds []domain.EventKind
		for ev, err := range stream {
			if err != nil {
				t.Error(err)
				return kinds
			}
			kinds = append(kinds, ev.Kind)
		}
		return kinds
	}
	a := make(chan []domain.EventKind)
	go func() { a <- first() }()
	time.Sleep(20 * time.Millisecond)
	b := first()
	ka := <-a
	if len(ka) == 0 || ka[0] == domain.EventQueued {
		t.Fatalf("a call with a free place began %v", ka)
	}
	if len(b) < 3 || b[0] != domain.EventQueued || b[1] != domain.EventSent || b[2] == domain.EventSent {
		t.Fatalf("a waiting call began %v, want queued, sent, then the provider's events", b)
	}
	for _, k := range ka {
		if k == domain.EventSent {
			t.Fatalf("a call with a free place said sent: %v", ka)
		}
	}
}

// TestATimedOutCallHoldsItsPlaceUntilItsRequestCloses_REQ_ORC_005: a call abandoned at its
// first-token timeout keeps its provider's place until the provider's stream has returned, so
// the next call is never sent beside a request still open.
func TestATimedOutCallHoldsItsPlaceUntilItsRequestCloses_REQ_ORC_005(t *testing.T) {
	t.Parallel()

	local := &streamProvider{id: "ollama", local: true, scripts: map[string]script{
		// Stream holds until cancelled, then lingers before returning: a slow close.
		"slow": {holdOpen: true, lingerAfterCancel: 100 * time.Millisecond}, "m": {events: answer},
	}}
	r := newRig(t, false, map[string][]string{"slow": {"ollama/slow"}, "code": {"ollama/m"}},
		[]*streamProvider{local}, []domain.Model{model("ollama", "slow"), model("ollama", "m")})

	go func() { _, _ = collect(t, r.router, hello("slow")) }()
	time.Sleep(20 * time.Millisecond)
	if _, err := collect(t, r.router, hello("code")); err != nil {
		t.Fatal(err)
	}
	if got := local.mostAtOnce(); got != 1 {
		t.Fatalf("%d requests were open at the local provider at once, want 1", got)
	}
}
