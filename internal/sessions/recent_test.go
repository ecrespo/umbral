package sessions

import (
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// TestOnlyAThreadsTerminalKeepsRecentOutput_REQ_FTU_001: the output a wait after keys reads is
// kept for a thread's own terminal only, at most maxRecentBytes of it; a user's pane keeps none
// (delta `2026-10-usf2-002-live`).
func TestOnlyAThreadsTerminalKeepsRecentOutput_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	pane := &liveSession{session: domain.Session{ID: "ses_1"}}
	pane.keepRecent(1, []byte("hello"))
	if pane.recentBytes != 0 || pane.outputAfter(0) != "" {
		t.Errorf("a user's pane kept %d bytes", pane.recentBytes)
	}

	thread := &liveSession{session: domain.Session{ID: "ses_2", OwnerThreadID: "thr_1"}}
	chunk := []byte(strings.Repeat("x", 64<<10))
	for seq := uint64(1); seq <= 8; seq++ {
		thread.keepRecent(seq, chunk)
	}
	thread.keepRecent(9, []byte("tail"))
	if thread.recentBytes > maxRecentBytes {
		t.Errorf("a thread's terminal kept %d bytes, over %d", thread.recentBytes, maxRecentBytes)
	}
	if got := thread.outputAfter(8); got != "tail" {
		t.Errorf("output after 8 = %q, want %q", got, "tail")
	}
}
