package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/notify/domain"
)

// fakeDeliverer records what reached "the foreground client" and says whether one exists.
type fakeDeliverer struct {
	mu        sync.Mutex
	present   bool
	delivered []domain.Notification
}

func (f *fakeDeliverer) DeliverForeground(n domain.Notification) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present {
		return false
	}
	f.delivered = append(f.delivered, n)
	return true
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newService(t *testing.T, enabled bool, d *fakeDeliverer, c *clock) *Service {
	t.Helper()
	svc, err := New(Config{Enabled: enabled, Now: c.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc.SetDeliverer(d)
	return svc
}

func TestNotificationNormalizesAndReports_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	d := &fakeDeliverer{present: true}
	svc := newService(t, true, d, c)

	res, err := svc.Show(ctx, domain.Request{
		Title: "\x1b[31mtests\x1b[0m\t passed", Body: "  all\n green  ", Source: "ci-hook", Sound: domain.SoundDone,
	})
	if err != nil || !res.Shown || res.Reason != domain.ReasonShown {
		t.Fatalf("Show = %+v, %v; want shown", res, err)
	}
	if len(d.delivered) != 1 {
		t.Fatalf("delivered %d notifications, want 1", len(d.delivered))
	}
	got := d.delivered[0]
	if got.Title != "[31mtests[0m passed" || got.Body != "all green" || got.Source != "ci-hook" || got.Sound != domain.SoundDone {
		t.Errorf("delivered %+v: the text must be normalized before it reaches a client", got)
	}

	// No interactive client: nothing is delivered, and the caller is told why.
	d.present = false
	if res, err := svc.Show(ctx, domain.Request{Title: "t", Source: "ci-hook"}); err != nil || res.Shown || res.Reason != domain.ReasonNoClient {
		t.Errorf("without a client Show = %+v, %v; want no_client", res, err)
	}

	// Validation errors are errors, not reasons.
	if _, err := svc.Show(ctx, domain.Request{Title: "", Source: "ci-hook"}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("an empty title = %v, want ErrValidation", err)
	}

	// Disabled: nothing is delivered and nothing is counted against the source.
	d.present = true
	off := newService(t, false, d, c)
	before := len(d.delivered)
	for range 7 {
		if res, err := off.Show(ctx, domain.Request{Title: "t", Source: "ci-hook"}); err != nil || res.Shown || res.Reason != domain.ReasonDisabled {
			t.Fatalf("disabled Show = %+v, %v; want disabled", res, err)
		}
	}
	if len(d.delivered) != before {
		t.Errorf("a disabled service delivered %d notifications", len(d.delivered)-before)
	}

	// A service with no deliverer yet behaves as if no client were connected.
	bare, _ := New(Config{Enabled: true, Now: c.Now})
	if res, err := bare.Show(ctx, domain.Request{Title: "t", Source: "x"}); err != nil || res.Reason != domain.ReasonNoClient {
		t.Errorf("no deliverer: Show = %+v, %v; want no_client", res, err)
	}
}

func TestNotificationRateLimited_REQ_NTF_002(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	d := &fakeDeliverer{present: true}
	svc := newService(t, true, d, c)

	show := func(source string) domain.Reason {
		t.Helper()
		res, err := svc.Show(ctx, domain.Request{Title: "t", Source: source})
		if err != nil {
			t.Fatalf("Show(%s): %v", source, err)
		}
		return res.Reason
	}

	for i := range domain.RateLimit {
		if r := show("noisy"); r != domain.ReasonShown {
			t.Fatalf("request %d of noisy = %s, want shown", i+1, r)
		}
		c.now = c.now.Add(time.Second)
	}
	if r := show("noisy"); r != domain.ReasonRateLimited {
		t.Fatalf("the sixth request within 60 s = %s, want rate_limited", r)
	}
	// Another source is not held back by the noisy one.
	if r := show("quiet"); r != domain.ReasonShown {
		t.Errorf("another source = %s, want shown", r)
	}
	// A discarded request is not delivered.
	if n := len(d.delivered); n != domain.RateLimit+1 {
		t.Errorf("delivered %d, want %d", n, domain.RateLimit+1)
	}
	// The window slides: once the first request is 60 s old, one more fits.
	c.now = time.Unix(1_700_000_000, 0).Add(domain.RateWindow)
	if r := show("noisy"); r != domain.ReasonShown {
		t.Errorf("after the oldest left the window = %s, want shown", r)
	}
	if r := show("noisy"); r != domain.ReasonRateLimited {
		t.Errorf("the window holds five again = %s, want rate_limited", r)
	}
	// A request with no client still counts: the limit is on what a source asks for.
	d.present = false
	c.now = c.now.Add(2 * domain.RateWindow)
	for range domain.RateLimit {
		show("absent")
	}
	if r := show("absent"); r != domain.ReasonRateLimited {
		t.Errorf("requests with no client must count too; got %s", r)
	}
}

func TestShowIsSafeConcurrently_REQ_NTF_002(t *testing.T) {
	t.Parallel()
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	d := &fakeDeliverer{present: true}
	svc := newService(t, true, d, c)

	var wg sync.WaitGroup
	var mu sync.Mutex
	shown := 0
	for range 50 {
		wg.Go(func() {
			res, err := svc.Show(context.Background(), domain.Request{Title: "t", Source: "burst"})
			if err == nil && res.Shown {
				mu.Lock()
				shown++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if shown != domain.RateLimit {
		t.Errorf("%d of 50 concurrent requests were shown, want exactly %d", shown, domain.RateLimit)
	}
}

// blockingDeliverer holds its first delivery until released, as a client that stopped
// reading would.
type blockingDeliverer struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingDeliverer) DeliverForeground(domain.Notification) bool {
	first := false
	b.once.Do(func() { first = true })
	if first {
		close(b.entered)
		<-b.release
	}
	return true
}

// TestASlowDeliveryDoesNotBlockOtherSources_REQ_NTF_002: the sink decides and counts under
// its lock and delivers after letting go, so a delivery that hangs leaves every other
// source free to be answered.
func TestASlowDeliveryDoesNotBlockOtherSources_REQ_NTF_002(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, err := New(Config{Enabled: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := &blockingDeliverer{entered: make(chan struct{}), release: make(chan struct{})}
	svc.SetDeliverer(d)
	defer close(d.release)

	go func() { _, _ = svc.Show(ctx, domain.Request{Title: "slow", Source: "hook-a"}) }()
	<-d.entered

	done := make(chan domain.Result, 1)
	go func() {
		res, _ := svc.Show(ctx, domain.Request{Title: "fast", Source: "hook-b"})
		done <- res
	}()
	select {
	case res := <-done:
		if !res.Shown {
			t.Errorf("the other source got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a delivery that hangs blocked another source")
	}
}
