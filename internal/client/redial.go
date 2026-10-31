package client

import (
	"context"
	"errors"
	"time"
)

// RedialWindow is how long a client that lost its daemon keeps dialling. A live handoff
// (REQ-HND-001) leaves the socket without a server for milliseconds, and the old daemon gives a
// stalled handoff 30 s before it resumes; a client that waits as long as that sees either
// daemon come back.
const RedialWindow = 30 * time.Second

// redialAttempt bounds one dial: a daemon that holds a connection without answering is cut
// and tried again rather than holding the whole window.
const redialAttempt = 5 * time.Second

// The backoff between attempts, doubling from the first to the cap.
const (
	redialFirstWait = 50 * time.Millisecond
	redialMaxWait   = time.Second
)

// Redial calls dial until it succeeds, ctx ends or window has passed, waiting longer between
// attempts each time. A failure is the last attempt's error, or ctx's.
func Redial[T any](ctx context.Context, window time.Duration, dial func(context.Context) (T, error)) (T, error) {
	var zero T
	deadline := time.Now().Add(window)
	wait := redialFirstWait
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			if last == nil {
				last = errors.New("client: no time left to dial the daemon")
			}
			return zero, last
		}
		attempt, cancel := context.WithTimeout(ctx, min(left, redialAttempt))
		got, err := dial(attempt)
		cancel()
		if err == nil {
			return got, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(min(wait, max(time.Until(deadline), 0))):
		}
		wait = min(2*wait, redialMaxWait)
	}
}
