// Package slots is a counting semaphore that grants in arrival order. The agent runtime
// holds turns over `[agents] max_concurrent_turns` with it (REQ-ORC-002), and the model
// router queues calls per provider with it (REQ-ORC-005). It imports the standard library
// only, so either module may use it without reaching the other.
package slots

import (
	"context"
	"sync"
)

// Queue grants at most its limit of tickets at once; a limit of 0 or less grants every
// ticket. A ticket that cannot be granted waits behind every older one: a release grants
// the oldest waiter, never a newer ticket.
type Queue struct {
	mu      sync.Mutex
	limit   int
	held    int
	waiting []*Ticket
}

// New makes a queue with the limit.
func New(limit int) *Queue { return &Queue{limit: limit} }

// Ticket is one place in a queue.
type Ticket struct {
	q     *Queue
	ready chan struct{}
	// state is guarded by q.mu.
	state ticketState
}

type ticketState int

const (
	ticketWaiting ticketState = iota
	ticketHeld
	ticketDone
)

// Enqueue takes a place: granted at once when the queue has a free slot and nobody waits,
// otherwise behind every ticket already waiting.
func (q *Queue) Enqueue() *Ticket {
	t := &Ticket{q: q, ready: make(chan struct{})}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiting) == 0 && q.free() {
		q.grant(t)
	} else {
		q.waiting = append(q.waiting, t)
	}
	return t
}

// SetLimit changes the limit. A higher one grants waiters at once; a lower one lets the
// tickets already held finish and grants nothing until the queue is under it.
func (q *Queue) SetLimit(limit int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limit = limit
	q.next()
}

// Held is how many tickets hold a slot now.
func (q *Queue) Held() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.held
}

func (q *Queue) free() bool { return q.limit <= 0 || q.held < q.limit }

// grant gives t a slot. q.mu is held.
func (q *Queue) grant(t *Ticket) {
	q.held++
	t.state = ticketHeld
	close(t.ready)
}

// next grants waiters, oldest first, while there is a free slot. q.mu is held.
func (q *Queue) next() {
	for len(q.waiting) > 0 && q.free() {
		t := q.waiting[0]
		q.waiting = q.waiting[1:]
		q.grant(t)
	}
}

// Granted reports whether the ticket holds its slot now.
func (t *Ticket) Granted() bool {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.state == ticketHeld
}

// Wait returns nil once the ticket holds its slot, or ctx's error once ctx is done; a ticket
// given up that way has left the queue and holds nothing. Either way the owner calls Release
// when it is finished with the ticket.
func (t *Ticket) Wait(ctx context.Context) error {
	select {
	case <-t.ready:
		return nil
	case <-ctx.Done():
	}
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	if t.state == ticketHeld {
		// Granted as the context ended: the caller sees an error, so it will not use the
		// slot, and the slot goes to the next waiter.
		t.releaseLocked()
	} else {
		t.dropLocked()
	}
	return ctx.Err()
}

// Release frees the ticket's slot, or leaves the queue if it is still waiting. A second
// Release does nothing.
func (t *Ticket) Release() {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	switch t.state {
	case ticketHeld:
		t.releaseLocked()
	case ticketWaiting:
		t.dropLocked()
	case ticketDone:
	}
}

func (t *Ticket) releaseLocked() {
	t.state = ticketDone
	t.q.held--
	t.q.next()
}

func (t *Ticket) dropLocked() {
	t.state = ticketDone
	for i, w := range t.q.waiting {
		if w == t {
			t.q.waiting = append(t.q.waiting[:i], t.q.waiting[i+1:]...)
			return
		}
	}
}
