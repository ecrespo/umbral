package integration_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// TestReportRateDegradesWithoutError_REQ_AUT_005: a source keeps at most 10 reports in any
// second, state and metadata together. The excess answers like a kept report, changes
// nothing and is counted; a malformed report is refused before the rate and does not use it
// up; another source has its own window; the window slides; release_state is not limited
// (delta `2026-10-wait-monitoring`, decision 9).
func TestReportRateDegradesWithoutError_REQ_AUT_005(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	now := time.UnixMilli(1757592000000)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	h := newHarnessAt(t, clock)
	paneID := h.newPane(t).RootPane.ID

	state := func(source string, st domain.State) error {
		return h.ReportState(t.Context(), domain.Report{PaneID: paneID, Source: source, State: st})
	}
	// A malformed report first: refused, and it costs the source nothing.
	if err := state("hook", "sleepy"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a malformed report: %v, want a validation error", err)
	}
	for i := range 5 {
		if err := state("hook", domain.StateWorking); err != nil {
			t.Fatalf("report %d: %v", i+1, err)
		}
	}
	for i := range 5 { // metadata shares the window
		if err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: paneID, Source: "hook", Title: str("t"), TitleSent: true}); err != nil {
			t.Fatalf("metadata %d: %v", i+1, err)
		}
	}
	if n := h.limited.Load(); n != 0 {
		t.Fatalf("%d reports discarded inside the rate", n)
	}
	// The 11th in the same second: answered, discarded, counted.
	if err := state("hook", domain.StateBlocked); err != nil {
		t.Fatalf("a report past the rate answered %v, want the normal answer", err)
	}
	if err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: paneID, Source: "hook", Title: str("late"), TitleSent: true}); err != nil {
		t.Fatalf("metadata past the rate answered %v", err)
	}
	if p := h.pane(t, paneID); p.AttentionState == "blocked" || p.Metadata.Title == "late" {
		t.Fatalf("a report past the rate was applied: state %s, title %q", p.AttentionState, p.Metadata.Title)
	}
	if n := h.limited.Load(); n != 2 {
		t.Fatalf("discarded reports counted %d, want 2", n)
	}
	// Past the rate a report is still checked: an unknown pane is NOT_FOUND, not discarded.
	if err := h.ReportState(t.Context(), domain.Report{PaneID: "w99:p9", Source: "hook", State: domain.StateWorking}); !errors.Is(err, wsdomain.ErrNotFound) {
		t.Fatalf("an unknown pane past the rate answered %v, want not found", err)
	}
	// A stale report keeps nothing, so it uses up no place in its source's window.
	if err := h.ReportState(t.Context(), domain.Report{PaneID: paneID, Source: "seqd", State: domain.StateWorking, Seq: seq(5)}); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if err := h.ReportState(t.Context(), domain.Report{PaneID: paneID, Source: "seqd", State: domain.StateBlocked, Seq: seq(1)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.limited.Load(); n != 2 {
		t.Fatalf("stale reports used up the window: %d discarded, want 2", n)
	}
	// Another source is not affected, and a release is never limited.
	if err := state("other", domain.StateDone); err != nil {
		t.Fatal(err)
	}
	if p := h.pane(t, paneID); p.StateSource != "other" {
		t.Fatalf("another source's report was not applied: source %q", p.StateSource)
	}
	if err := h.ReleaseState(t.Context(), paneID, "hook"); err != nil {
		t.Fatalf("release past the rate: %v", err)
	}
	// A second later the window has slid and the source is heard again.
	advance(time.Second)
	if err := state("hook", domain.StateBlocked); err != nil {
		t.Fatal(err)
	}
	if p := h.pane(t, paneID); p.AttentionState != "blocked" || p.StateSource != "hook" {
		t.Fatalf("after the window slid: state %s from %q", p.AttentionState, p.StateSource)
	}
	if n := h.limited.Load(); n != 2 {
		t.Errorf("discarded reports counted %d after the window slid, want 2", n)
	}
}
