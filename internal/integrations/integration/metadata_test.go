package integration_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

func str(v string) *string { return &v }

func ttl(ms int64) *int64 { return &ms }

// TestMetadataNeverChangesWaits_REQ_INT_004: display metadata — a title, a displayed name,
// state labels and tokens with a TTL — is shown on the pane and kept apart from its semantic
// state. Whatever it says, even a label for `blocked`, the pane's state, its source, the
// workspace rollup and an own-agent thread's attention (what thread waits observe) do not
// move, and no `pane.state_changed` or `workspace.updated` is published: only `pane.updated`,
// which is how a client redraws. The limits refuse a report whole, and an expired key is no
// longer read.
func TestMetadataNeverChangesWaits_REQ_INT_004(t *testing.T) {
	t.Parallel()
	h := newHarnessAt(t, time.Now)
	tree := h.newPane(t)
	paneID := tree.RootPane.ID
	// An own-agent pane, so "waits" is concrete: a thread wait observes this attention.
	h.exec(t, `INSERT INTO threads(id, cwd, attention_state, created_at, updated_at)
	           VALUES ('thr_1', '/tmp', 'working', 1, 1)`)
	h.exec(t, `UPDATE sessions SET owner_thread_id = 'thr_1' WHERE id = ?`, tree.RootPane.SessionID)
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h.drain()
	before := h.pane(t, paneID)

	if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
		PaneID: paneID, Source: "claude-code",
		Title: str("go\ttest  ./..."), TitleSent: true,
		DisplayName: str("tests \x1b[31mred"), DisplayNameSent: true,
		StateLabels: map[string]*string{"blocked": str("needs approval"), "done": str("finished")},
		Tokens:      map[string]*string{"summary": str("unit")},
	}); err != nil {
		t.Fatalf("ReportMetadata on an agent's pane: %v (REQ-INT-006 leaves it open to metadata)", err)
	}

	after := h.pane(t, paneID)
	want := wsdomain.Metadata{
		Title: "go test ./...", DisplayName: "tests [31mred",
		StateLabels: map[string]string{"blocked": "needs approval", "done": "finished"},
		Tokens:      map[string]string{"summary": "unit"},
	}
	if fmt.Sprint(after.Metadata) != fmt.Sprint(want) {
		t.Errorf("metadata = %+v, want %+v", after.Metadata, want)
	}
	if after.AttentionState != before.AttentionState || after.StateSource != before.StateSource ||
		after.AttentionState != wsdomain.AttentionWorking || after.StateSource != wsdomain.SourceAgent {
		t.Errorf("metadata moved the state: (%s, %q) -> (%s, %q)",
			before.AttentionState, before.StateSource, after.AttentionState, after.StateSource)
	}
	var attention string
	if err := h.store.DB().QueryRowContext(t.Context(),
		`SELECT attention_state FROM threads WHERE id = 'thr_1'`).Scan(&attention); err != nil || attention != "working" {
		t.Errorf("the thread's attention is %q (%v), want working", attention, err)
	}
	workspaces, err := h.tree.ListWorkspaces(t.Context())
	if err != nil || workspaces[0].RollupState != wsdomain.AttentionWorking {
		t.Errorf("the rollup is %+v (%v), want working", workspaces, err)
	}
	events := h.drain()
	if len(stateChanges(events)) != 0 || len(rollups(events)) != 0 {
		t.Errorf("metadata published semantic events: %+v", events)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one pane.updated", events)
	}
	if update, ok := events[0].(wsports.PaneEvent); !ok || update.Kind != wsports.KindPaneUpdated || update.Pane.Metadata.Title != "go test ./..." {
		t.Errorf("the event is %+v, want pane.updated carrying the metadata", events[0])
	}
	// A refresh from the semantic side announces nothing either: metadata is not an input.
	if err := h.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if events := h.drain(); len(events) != 0 {
		t.Errorf("a refresh after metadata published %+v", events)
	}

	t.Run("null clears, absent keeps", func(t *testing.T) {
		if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
			PaneID: paneID, Source: "claude-code", DisplayNameSent: true,
			Tokens: map[string]*string{"summary": nil},
		}); err != nil {
			t.Fatalf("ReportMetadata: %v", err)
		}
		m := h.pane(t, paneID).Metadata
		if m.Title != "go test ./..." || m.DisplayName != "" || m.Tokens != nil || m.StateLabels["blocked"] != "needs approval" {
			t.Errorf("metadata = %+v", m)
		}
	})

	t.Run("a source clears only its own keys", func(t *testing.T) {
		if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
			PaneID: paneID, Source: "other", Title: str("other's"), TitleSent: true,
			StateLabels: map[string]*string{"idle": str("resting")},
		}); err != nil {
			t.Fatalf("ReportMetadata: %v", err)
		}
		if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
			PaneID: paneID, Source: "claude-code", TitleSent: true, StateLabelsNull: true,
		}); err != nil {
			t.Fatalf("ReportMetadata with null title and state_labels: %v", err)
		}
		m := h.pane(t, paneID).Metadata
		if m.Title != "other's" || fmt.Sprint(m.StateLabels) != "map[idle:resting]" {
			t.Errorf("metadata = %+v, want only the other source's title and label", m)
		}
		h.exec(t, `DELETE FROM pane_metadata WHERE source = 'other'`)
		if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
			PaneID: paneID, Source: "claude-code", Title: str("go test ./..."), TitleSent: true,
			StateLabels: map[string]*string{"blocked": str("needs approval")},
		}); err != nil {
			t.Fatalf("ReportMetadata: %v", err)
		}
	})

	t.Run("a key without ttl_ms lives a day", func(t *testing.T) {
		var lifetime int64
		if err := h.store.DB().QueryRowContext(t.Context(), `
			SELECT expires_at - updated_at FROM pane_metadata
			 WHERE pane_id = ? AND source = 'claude-code' AND key = 'title'`, paneID).Scan(&lifetime); err != nil {
			t.Fatalf("read the title's expiry: %v", err)
		}
		if lifetime != domain.MaxTTLMillis {
			t.Errorf("a key without ttl_ms expires %d ms after its write, want %d", lifetime, domain.MaxTTLMillis)
		}
	})

	t.Run("the limits refuse a report whole", func(t *testing.T) {
		tokens := map[string]*string{}
		for i := range domain.MaxTokensPerReport + 1 {
			tokens[fmt.Sprintf("k%d", i)] = str("v")
		}
		err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: paneID, Source: "a", Tokens: tokens})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("17 tokens in one report: %v, want a validation error", err)
		}
		// Two live keys so far (a title and a label); two sources of 16 take it to 34.
		for _, source := range []string{"a", "b"} {
			tokens := map[string]*string{}
			for i := range domain.MaxTokensPerReport {
				tokens[fmt.Sprintf("%s%d", source, i)] = str("v")
			}
			err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: paneID, Source: source, Tokens: tokens})
			switch source {
			case "a":
				if err != nil {
					t.Fatalf("18 live keys: %v", err)
				}
			case "b":
				if !errors.Is(err, domain.ErrValidation) {
					t.Fatalf("34 live keys: %v, want a validation error", err)
				}
			}
		}
		if n := len(h.pane(t, paneID).Metadata.Tokens); n != domain.MaxTokensPerReport {
			t.Errorf("%d tokens after the refused report, want the %d of the accepted one", n, domain.MaxTokensPerReport)
		}
		h.exec(t, `DELETE FROM pane_metadata WHERE source = 'a'`)
	})

	t.Run("a stale seq changes nothing", func(t *testing.T) {
		report := func(title string, n int64) {
			t.Helper()
			if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
				PaneID: paneID, Source: "numbered", Title: str(title), TitleSent: true, Seq: &n,
			}); err != nil {
				t.Fatalf("ReportMetadata(%s, %d): %v", title, n, err)
			}
		}
		report("first", 0)
		report("second", 0)
		if m := h.pane(t, paneID).Metadata; m.Title != "first" {
			t.Errorf("title = %q after a repeated seq, want first", m.Title)
		}
		report("third", 1)
		if m := h.pane(t, paneID).Metadata; m.Title != "third" {
			t.Errorf("title = %q, want third", m.Title)
		}
	})

	t.Run("an expired key is not read", func(t *testing.T) {
		if err := h.ReportMetadata(t.Context(), domain.MetadataFields{
			// A TTL wide enough that a slow runner under -race still reads the token live:
			// 150 ms expired between the write and the read on macOS CI.
			PaneID: paneID, Source: "short", Tokens: map[string]*string{"eta": str("2s")}, TTLMillis: ttl(1500),
		}); err != nil {
			t.Fatalf("ReportMetadata with a TTL: %v", err)
		}
		if m := h.pane(t, paneID).Metadata; m.Tokens["eta"] != "2s" {
			t.Fatalf("a live token is not read: %+v", m)
		}
		deadline := time.Now().Add(8 * time.Second)
		for h.pane(t, paneID).Metadata.Tokens["eta"] != "" {
			if time.Now().After(deadline) {
				t.Fatal("the token outlived its TTL by seconds")
			}
			time.Sleep(25 * time.Millisecond)
		}
	})

	t.Run("a missing pane is NOT_FOUND", func(t *testing.T) {
		err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: "w9:p9", Source: "x", Title: str("t"), TitleSent: true})
		if !errors.Is(err, wsdomain.ErrNotFound) {
			t.Errorf("ReportMetadata on an unknown pane: %v", err)
		}
	})
}

// TestMetadataFollowsAMove_REQ_INT_004: a moved pane keeps its metadata, reached by its alias.
func TestMetadataFollowsAMove_REQ_INT_004(t *testing.T) {
	t.Parallel()
	h := newHarnessAt(t, time.Now)
	tree := h.newPane(t)
	split, _, err := h.tree.SplitPane(t.Context(), wsdomain.SplitParams{PaneID: tree.RootPane.ID, Direction: wsdomain.SplitRight})
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: split.ID, Source: "x", Title: str("before"), TitleSent: true}); err != nil {
		t.Fatalf("ReportMetadata: %v", err)
	}
	moved, err := h.tree.MovePane(t.Context(), wsdomain.MoveParams{
		PaneID: split.ID, Destination: wsdomain.MoveDestination{Type: wsdomain.MoveToNewWorkspace},
	})
	if err != nil {
		t.Fatalf("MovePane: %v", err)
	}
	if moved.Pane.Metadata.Title != "before" {
		t.Errorf("the moved pane's title is %q", moved.Pane.Metadata.Title)
	}
	if err := h.ReportMetadata(t.Context(), domain.MetadataFields{PaneID: split.ID, Source: "x", Title: str("after"), TitleSent: true}); err != nil {
		t.Fatalf("ReportMetadata by the old id: %v", err)
	}
	if m := h.pane(t, moved.Pane.ID).Metadata; m.Title != "after" {
		t.Errorf("title = %q, want after", m.Title)
	}
}
