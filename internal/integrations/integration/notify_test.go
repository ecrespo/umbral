package integration_test

import (
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/integrations/ports"
)

// TestExternalBlockedRaisesANotification_REQ_NTF_001 is delta `2026-10-integration-surface`'s
// decision 15: an external report that makes a pane `blocked` raises one user notification
// under the reporter's source. `done`, `working` and `idle` raise none, nor does a report
// that leaves the pane as it was, nor a stale one. With several sources the one whose
// report decides the pane is the one named.
func TestExternalBlockedRaisesANotification_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	paneID := h.newPane(t).RootPane.ID
	report := func(source string, state domain.State, seq int64, message string) {
		t.Helper()
		s := seq
		if err := h.ReportState(t.Context(), domain.Report{
			PaneID: paneID, Source: source, Agent: "claude", State: state, Message: message, Seq: &s,
		}); err != nil {
			t.Fatalf("ReportState(%s, %s): %v", source, state, err)
		}
	}

	report("claude-code", domain.StateWorking, 1, "")
	report("claude-code", domain.StateDone, 2, "")
	report("claude-code", domain.StateIdle, 3, "")
	if sent := h.notifier.all(); len(sent) != 0 {
		t.Fatalf("working, done and idle raised %v", sent)
	}

	report("claude-code", domain.StateBlocked, 4, "approve the edit?")
	want := ports.BlockedNotice{PaneID: paneID, Source: "claude-code", Agent: "claude", Message: "approve the edit?"}
	if sent := h.notifier.all(); len(sent) != 1 || sent[0] != want {
		t.Fatalf("a blocked report raised %+v, want [%+v]", sent, want)
	}

	// Still blocked: the pane did not change, so the user is not told again.
	report("claude-code", domain.StateBlocked, 5, "still waiting")
	// Stale: discarded, so nothing changed either.
	report("claude-code", domain.StateBlocked, 1, "replayed")
	if n := len(h.notifier.all()); n != 1 {
		t.Fatalf("an unchanged or stale blocked raised %d notifications, want 1", n)
	}

	// Another source's newer report decides the pane, and its blocked names it.
	report("codex", domain.StateWorking, 1, "")
	report("claude-code", domain.StateWorking, 6, "")
	report("codex", domain.StateBlocked, 2, "codex asks")
	if sent := h.notifier.all(); len(sent) != 2 || sent[1].Source != "codex" {
		t.Fatalf("codex's blocked raised %+v", sent)
	}
}

// TestABlockedPaneChangingSourceIsNotifiedAgain: a pane already blocked under one source
// that another source's newer report keeps blocked is a new request from someone else, so
// the user is told who asks now (delta `2026-10-notifications`, decision 10).
func TestABlockedPaneChangingSourceIsNotifiedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	paneID := h.newPane(t).RootPane.ID
	for i, source := range []string{"claude-code", "codex"} {
		s := int64(1)
		if err := h.ReportState(t.Context(), domain.Report{
			PaneID: paneID, Source: source, State: domain.StateBlocked, Seq: &s,
		}); err != nil {
			t.Fatalf("ReportState(%s): %v", source, err)
		}
		if sent := h.notifier.all(); len(sent) != i+1 || sent[i].Source != source {
			t.Fatalf("after %s's blocked: %+v", source, sent)
		}
	}
}

// TestTheNotifierIsCalledWithoutTheServiceLock: the sink is called after the service lets
// go of its lock, so a slow notification never stalls another hook's report (REQ-NTF-002:
// "without blocking other sources"). The notifier calls back into the service; if the lock
// were still held, the callback would deadlock.
func TestTheNotifierIsCalledWithoutTheServiceLock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	paneID := h.newPane(t).RootPane.ID
	h.notifier.mu.Lock()
	h.notifier.onNotify = func() { _ = h.ReleaseState(t.Context(), paneID, "someone-else") }
	h.notifier.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		s := int64(1)
		done <- h.ReportState(t.Context(), domain.Report{
			PaneID: paneID, Source: "claude-code", State: domain.StateBlocked, Seq: &s,
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReportState: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notifier ran under the service's lock: a call back into the service deadlocked")
	}
}
