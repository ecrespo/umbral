package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// isInputLocked reports whether err is the lock refusal of REQ-TERM-008.
func isInputLocked(err error) bool { return errors.Is(err, domain.ErrInputLocked) }

// containsBytes reports whether a VT snapshot contains the given text.
func containsBytes(snapshot []byte, want string) bool {
	return strings.Contains(string(snapshot), want)
}

// eventuallyContains polls the session's screen until the text appears. A shell takes an
// unpredictable moment to echo and run a command, so the alternative is a sleep long
// enough to be slow and short enough to be flaky.
func eventuallyContains(t *testing.T, h *harness, sessionID, want string) bool {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := h.Snapshot(t.Context(), sessionID)
		if err == nil && containsBytes(snapshot.Data, want) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
