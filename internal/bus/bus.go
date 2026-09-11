// Package bus is the daemon's typed in-process pub/sub (Tech Design §3).
//
// Modules never call each other directly for events: sessions publishes, api and agents
// subscribe. That is what keeps sessions from importing agents, which Art. 3 forbids and
// `task arch` enforces.
//
// Delivery is per-subscription, buffered and lossy under pressure: a subscriber that
// stops reading has its oldest events dropped and its drop counter raised, rather than
// blocking the publisher. A stalled TUI must never stall the PTY reader that feeds it.
// The client-facing backpressure policy, an 8 MiB queue that unsubscribes on overflow,
// belongs to the API fan-out in T-F0-06; this is the in-process layer beneath it.
package bus

import (
	"sync"
	"sync/atomic"
)

// Kind names an event type. Values mirror the notification names in API Spec §6, so a
// reader can follow one event from the bus to the wire without a translation table.
type Kind string

// Event is anything the bus carries. Implementations live in each module's domain, which
// is why bus depends on domain packages and never the other way round.
type Event interface {
	// EventKind reports the event's kind, used for subscription filtering.
	EventKind() Kind
}

// DefaultBuffer is how many events a subscription holds before the oldest are dropped.
const DefaultBuffer = 256

// Bus is a many-to-many publisher. The zero value is not usable; call New.
type Bus struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool
}

// New returns an empty bus.
func New() *Bus {
	return &Bus{subs: make(map[*Subscription]struct{})}
}

// Subscription is one subscriber's view of the bus. Close it when done; a leaked
// subscription keeps its buffer alive and silently accumulates drops.
type Subscription struct {
	bus     *Bus
	kinds   map[Kind]struct{} // nil means every kind
	ch      chan Event
	dropped atomic.Int64

	closeOnce sync.Once
}

// Subscribe returns a subscription to the given kinds. With no kinds it receives
// everything, which is what the API fan-out wants and what tests usually want.
func (b *Bus) Subscribe(kinds ...Kind) *Subscription {
	return b.SubscribeBuffered(DefaultBuffer, kinds...)
}

// SubscribeBuffered is Subscribe with an explicit buffer size. A buffer below 1 is
// raised to 1: a zero-length channel would turn every publish into a rendezvous and
// reintroduce exactly the coupling this package exists to avoid.
func (b *Bus) SubscribeBuffered(buffer int, kinds ...Kind) *Subscription {
	if buffer < 1 {
		buffer = 1
	}

	s := &Subscription{bus: b, ch: make(chan Event, buffer)}
	if len(kinds) > 0 {
		s.kinds = make(map[Kind]struct{}, len(kinds))
		for _, k := range kinds {
			s.kinds[k] = struct{}{}
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// A subscription to a closed bus is born closed, so the caller's range loop
		// ends immediately instead of blocking forever. It goes through closeOnce so a
		// later Close on it cannot close the channel twice.
		s.closeOnce.Do(func() { close(s.ch) })
		return s
	}
	b.subs[s] = struct{}{}
	return s
}

// Publish delivers ev to every interested subscription. It never blocks: a full
// subscription loses its oldest event and counts the loss.
func (b *Bus) Publish(ev Event) {
	if ev == nil {
		return
	}
	kind := ev.EventKind()

	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for s := range b.subs {
		if !s.wants(kind) {
			continue
		}
		s.deliver(ev)
	}
}

// Close shuts the bus down and closes every subscription channel. Publishing afterwards
// is a no-op, so a late goroutine cannot panic on a closed channel.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for s := range b.subs {
		s.closeOnce.Do(func() { close(s.ch) })
		delete(b.subs, s)
	}
}

// C is the subscription's receive channel. It is closed when the subscription or the bus
// is closed.
func (s *Subscription) C() <-chan Event { return s.ch }

// Dropped reports how many events this subscription lost because it was not reading fast
// enough. A non-zero value in production means the consumer needs a bigger buffer or a
// faster loop, and it is the number to put in a log line rather than guessing.
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

// Close unsubscribes. It is safe to call more than once and from any goroutine.
func (s *Subscription) Close() {
	b := s.bus

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, s)
	s.closeOnce.Do(func() { close(s.ch) })
}

// wants reports whether this subscription asked for the kind.
func (s *Subscription) wants(kind Kind) bool {
	if s.kinds == nil {
		return true
	}
	_, ok := s.kinds[kind]
	return ok
}

// deliver enqueues ev, discarding the oldest event if the buffer is full. The caller
// holds the bus read lock, which keeps Close from racing with the send.
func (s *Subscription) deliver(ev Event) {
	select {
	case s.ch <- ev:
		return
	default:
	}

	// Full: drop the oldest so the subscriber sees recent state rather than stale
	// state. Losing the oldest output chunk is better than stalling the PTY reader.
	select {
	case <-s.ch:
		s.dropped.Add(1)
	default:
	}
	select {
	case s.ch <- ev:
	default:
		// Another goroutine refilled the slot. Count the loss and move on.
		s.dropped.Add(1)
	}
}
