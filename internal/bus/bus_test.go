package bus

import (
	"sync"
	"testing"
	"time"
)

// testEvent is a minimal Event for these tests.
type testEvent struct {
	kind Kind
	n    int
}

func (e testEvent) EventKind() Kind { return e.kind }

const (
	kindOutput Kind = "session.output"
	kindExited Kind = "session.exited"
)

// recv reads one event, failing the test if none arrives.
func recv(t *testing.T, s *Subscription) Event {
	t.Helper()

	select {
	case ev, ok := <-s.C():
		if !ok {
			t.Fatal("subscription channel is closed")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return nil
	}
}

func TestPublishReachesEverySubscriber(t *testing.T) {
	t.Parallel()

	b := New()
	defer b.Close()

	first := b.Subscribe()
	second := b.Subscribe()

	b.Publish(testEvent{kind: kindOutput, n: 1})

	for name, s := range map[string]*Subscription{"first": first, "second": second} {
		if got := recv(t, s).(testEvent); got.n != 1 {
			t.Errorf("%s received %+v, want n=1", name, got)
		}
	}
}

func TestSubscribeFiltersByKind(t *testing.T) {
	t.Parallel()

	b := New()
	defer b.Close()

	only := b.Subscribe(kindExited)
	b.Publish(testEvent{kind: kindOutput, n: 1})
	b.Publish(testEvent{kind: kindExited, n: 2})

	if got := recv(t, only).(testEvent); got.n != 2 {
		t.Errorf("filtered subscription received %+v, want the exited event", got)
	}
	select {
	case ev := <-only.C():
		t.Errorf("filtered subscription also received %+v", ev)
	default:
	}
}

// TestSlowSubscriberDoesNotBlockThePublisher is the property the daemon depends on: a
// client that stops reading must not stall the goroutine draining a PTY.
func TestSlowSubscriberDoesNotBlockThePublisher(t *testing.T) {
	t.Parallel()

	b := New()
	defer b.Close()

	slow := b.SubscribeBuffered(2, kindOutput)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			b.Publish(testEvent{kind: kindOutput, n: i})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that was not reading")
	}

	if dropped := slow.Dropped(); dropped == 0 {
		t.Error("Dropped() = 0 after overflowing a 2-event buffer; losses must be counted, not hidden")
	}
	// The buffer keeps the most recent events, not the first ones.
	if got := recv(t, slow).(testEvent); got.n < 90 {
		t.Errorf("oldest buffered event is n=%d, want a recent one; the buffer kept stale state", got.n)
	}
}

func TestCloseSubscriptionStopsDelivery(t *testing.T) {
	t.Parallel()

	b := New()
	defer b.Close()

	s := b.Subscribe()
	s.Close()
	s.Close() // must be idempotent

	b.Publish(testEvent{kind: kindOutput, n: 1})

	if _, open := <-s.C(); open {
		t.Error("a closed subscription still delivered an event")
	}
}

func TestCloseBusClosesEverySubscription(t *testing.T) {
	t.Parallel()

	b := New()
	s := b.Subscribe()
	b.Close()
	b.Close() // must be idempotent

	if _, open := <-s.C(); open {
		t.Error("the subscription channel stayed open after the bus was closed")
	}

	// Subscribing to a closed bus yields a closed subscription rather than a hang,
	// and closing it afterwards must not panic on a double close.
	late := b.Subscribe()
	if _, open := <-late.C(); open {
		t.Error("subscribing to a closed bus returned an open channel")
	}
	late.Close()

	b.Publish(testEvent{kind: kindOutput, n: 1}) // must be a no-op, not a panic
}

// TestConcurrentPublishAndSubscribe is the reason Art. 2 mandates -race: the bus is the
// one place every module's goroutines meet.
func TestConcurrentPublishAndSubscribe(t *testing.T) {
	t.Parallel()

	b := New()
	defer b.Close()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				b.Publish(testEvent{kind: kindOutput, n: i})
			}
		}()
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := b.Subscribe(kindOutput)
			defer s.Close()
			for range 10 {
				select {
				case <-s.C():
				case <-time.After(time.Second):
					return
				}
			}
		}()
	}
	wg.Wait()
}
