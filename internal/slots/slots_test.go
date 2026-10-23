package slots

import (
	"context"
	"sync"
	"testing"
	"time"
)

func granted(t *Ticket) bool {
	select {
	case <-t.ready:
		return true
	default:
		return false
	}
}

func TestQueueGrantsUpToItsLimitInArrivalOrder(t *testing.T) {
	q := New(2)
	a, b, c, d := q.Enqueue(), q.Enqueue(), q.Enqueue(), q.Enqueue()
	if !a.Granted() || !b.Granted() {
		t.Fatal("the first two tickets were not granted at once")
	}
	if c.Granted() || d.Granted() {
		t.Fatal("a ticket over the limit was granted at once")
	}
	a.Release()
	if !granted(c) || granted(d) {
		t.Fatalf("a release granted c=%v d=%v, want the oldest waiter only", granted(c), granted(d))
	}
	b.Release()
	if !granted(d) {
		t.Fatal("the last waiter was not granted")
	}
}

func TestLoweringTheLimitTakesEffectAsSlotsFree(t *testing.T) {
	q := New(2)
	a, b := q.Enqueue(), q.Enqueue()
	q.SetLimit(1)
	c := q.Enqueue()
	a.Release()
	if granted(c) {
		t.Fatal("c was granted while b holds the one slot the new limit allows")
	}
	b.Release()
	if !granted(c) {
		t.Fatal("c was not granted once the queue was under its new limit")
	}
}

func TestWaitReturnsWhenGrantedOrCancelled(t *testing.T) {
	q := New(1)
	a := q.Enqueue()
	b := q.Enqueue()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.Wait(ctx); err == nil {
		t.Fatal("a cancelled wait returned no error")
	}
	// The cancelled ticket left the queue: the next one is granted on a's release.
	c := q.Enqueue()
	a.Release()
	if err := c.Wait(t.Context()); err != nil {
		t.Fatalf("c: %v", err)
	}
	if q.Held() != 1 {
		t.Fatalf("held %d, want 1", q.Held())
	}
}

func TestACancelledWaitGrantedMeanwhileFreesItsSlot(t *testing.T) {
	q := New(1)
	a := q.Enqueue()
	b := q.Enqueue()
	a.Release() // b is granted, but its waiter gives up anyway
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.Wait(ctx); err == nil {
		// Granted and cancelled are both ready; either answer is right, but a granted
		// ticket must then be released by its owner.
		b.Release()
	}
	if q.Held() != 0 {
		t.Fatalf("held %d after the only ticket gave up, want 0", q.Held())
	}
}

func TestReleaseIsIdempotentAndDropsAWaiter(t *testing.T) {
	q := New(1)
	a := q.Enqueue()
	b := q.Enqueue()
	b.Release() // leaves the queue without ever holding a slot
	a.Release()
	a.Release()
	if q.Held() != 0 {
		t.Fatalf("held %d, want 0", q.Held())
	}
	if c := q.Enqueue(); !c.Granted() {
		t.Fatal("a free queue did not grant")
	}
}

func TestRaisingTheLimitGrantsWaiters(t *testing.T) {
	q := New(1)
	q.Enqueue()
	b := q.Enqueue()
	q.SetLimit(2)
	if !granted(b) {
		t.Fatal("raising the limit granted no waiter")
	}
}

func TestZeroLimitIsUnbounded(t *testing.T) {
	q := New(0)
	for range 100 {
		if !q.Enqueue().Granted() {
			t.Fatal("an unbounded queue held a ticket")
		}
	}
}

func TestQueueUnderConcurrency(t *testing.T) {
	q := New(3)
	var mu sync.Mutex
	inside, peak := 0, 0
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			tk := q.Enqueue()
			if err := tk.Wait(t.Context()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			inside++
			peak = max(peak, inside)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			tk.Release()
		})
	}
	wg.Wait()
	if peak > 3 {
		t.Fatalf("%d held at once, over the limit of 3", peak)
	}
	if q.Held() != 0 {
		t.Fatalf("held %d at the end", q.Held())
	}
}
