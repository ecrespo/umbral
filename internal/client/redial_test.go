package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRedialTriesUntilADaemonAnswers_REQ_HND_001: a client that lost its daemon dials again,
// backing off, until one answers — the daemon that took the sessions over (REQ-HND-001).
func TestRedialTriesUntilADaemonAnswers_REQ_HND_001(t *testing.T) {
	attempts := 0
	got, err := Redial(t.Context(), 5*time.Second, func(context.Context) (int, error) {
		attempts++
		if attempts < 3 {
			return 0, errors.New("no daemon yet")
		}
		return 42, nil
	})
	if err != nil || got != 42 || attempts != 3 {
		t.Fatalf("Redial = %d, %v after %d attempts; want 42 on the third", got, err, attempts)
	}
}

// TestRedialGivesUpAfterItsWindow_REQ_HND_001: with no daemon to answer, the client stops
// within the window and says why, instead of retrying forever.
func TestRedialGivesUpAfterItsWindow_REQ_HND_001(t *testing.T) {
	refused := errors.New("refused")
	start := time.Now()
	_, err := Redial(t.Context(), 300*time.Millisecond, func(context.Context) (int, error) { return 0, refused })
	if !errors.Is(err, refused) {
		t.Fatalf("Redial ended with %v, want the last attempt's error", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Redial took %v with a 300 ms window", took)
	}
}

// TestRedialBoundsEachAttempt: a dial that hangs — a daemon accepting nothing — is cut at the
// attempt's bound and tried again, rather than holding the whole window.
func TestRedialBoundsEachAttempt(t *testing.T) {
	attempts := 0
	_, err := Redial(t.Context(), 15*time.Second, func(ctx context.Context) (int, error) {
		attempts++
		if attempts == 1 {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return 1, nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("Redial = %v after %d attempts, want success on the second", err, attempts)
	}
}

// TestRedialStopsWithItsContext: closing the client ends the redial at once.
func TestRedialStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Redial(ctx, time.Minute, func(context.Context) (int, error) { return 0, errors.New("no") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Redial with a cancelled context = %v", err)
	}
}
