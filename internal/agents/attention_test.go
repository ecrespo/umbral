package agents

import (
	"errors"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
)

// TestDoneUntilFocused_REQ_AGT_016: a turn nobody views ends `done` and stays `done` until a
// client focuses the thread, which makes it `idle`; a turn that ends while some client views
// the thread ends `idle` (API §7). Every change is persisted, then announced as
// thread.attention_changed.
func TestDoneUntilFocused_REQ_AGT_016(t *testing.T) {
	r := newRig(t, answer("one"), answer("two"), answer("three"), answer("four"))
	th := r.thread(t, domain.CreateParams{})
	ctx := t.Context()

	if _, end := r.send(t, th.ID, "first", ""); end.EndState != "done" {
		t.Fatalf("a turn nobody views ended %q, want done", end.EndState)
	}
	if got := r.attention(t, th.ID); got != "done" {
		t.Fatalf("after the turn: %q, want done", got)
	}
	if seen := r.store.seenAt(th.ID); seen != nil {
		t.Fatalf("seen_at = %d before any client focused the thread", *seen)
	}
	// Reading the thread is not viewing it: a script's thread.get must not clear `done`.
	if _, err := r.rt.Get(ctx, th.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.attention(t, th.ID); got != "done" {
		t.Fatalf("after a read: %q, want done", got)
	}

	if err := r.rt.Focus(ctx, "con_a", th.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.attention(t, th.ID); got != "idle" {
		t.Fatalf("after a focus: %q, want idle", got)
	}
	if r.store.seenAt(th.ID) == nil {
		t.Fatal("a focus left seen_at NULL")
	}
	if got := r.attentionEvents(th.ID); len(got) == 0 || got[len(got)-1] != "idle" {
		t.Fatalf("attention_changed after the focus: %v, want it to end with idle", got)
	}
	if got := r.rt.Focused(); got != th.ID {
		t.Fatalf("Focused() = %q, want %s", got, th.ID)
	}

	// Viewed: the turn ends idle directly, never passing through done.
	before := len(r.attentionEvents(th.ID))
	if _, end := r.send(t, th.ID, "second", ""); end.EndState != "idle" {
		t.Fatalf("a turn ended while viewed: %q, want idle", end.EndState)
	}
	if got := r.attentionEvents(th.ID)[before:]; !equal(got, []string{"working", "idle"}) {
		t.Fatalf("attention_changed during a viewed turn: %v, want [working idle]", got)
	}

	// Two viewers: one leaving does not stop the other's view.
	if err := r.rt.Focus(ctx, "con_b", th.ID); err != nil {
		t.Fatal(err)
	}
	r.rt.Unfocus("con_a")
	if _, end := r.send(t, th.ID, "third", ""); end.EndState != "idle" {
		t.Fatalf("a turn ended while one of two viewers is left: %q, want idle", end.EndState)
	}

	// Nobody views it any more: done again.
	if err := r.rt.Focus(ctx, "con_b", ""); err != nil {
		t.Fatal(err)
	}
	if got := r.rt.Focused(); got != "" {
		t.Fatalf("Focused() = %q after every viewer left, want none", got)
	}
	if _, end := r.send(t, th.ID, "fourth", ""); end.EndState != "done" {
		t.Fatalf("a turn ended after every viewer left: %q, want done", end.EndState)
	}

	if err := r.rt.Focus(ctx, "con_a", th.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.rt.Focus(ctx, "con_a", "thr_nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("focusing an unknown thread: %v, want ErrNotFound", err)
	}
	if got := r.rt.Focused(); got != th.ID {
		t.Fatalf("a refused focus changed the view to %q, want %s kept", got, th.ID)
	}
}

// TestAFocusOnAThreadNotDoneChangesNothing: focusing a thread that is idle, working or blocked
// records that it was seen and announces nothing, since its attention did not change.
func TestAFocusOnAThreadNotDoneChangesNothing(t *testing.T) {
	r := newRig(t, callTool("run", `{"path":"make"}`))
	th := r.thread(t, domain.CreateParams{})
	ctx := t.Context()

	if err := r.rt.Focus(ctx, "con_a", th.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.attentionEvents(th.ID); len(got) != 0 {
		t.Fatalf("focusing an idle thread announced %v", got)
	}
	if _, err := r.rt.Send(ctx, ports.SendParams{ThreadID: th.ID, Text: "build"}); err != nil {
		t.Fatal(err)
	}
	r.requested(t)
	if got := r.attentionEvents(th.ID); !equal(got, []string{"working", "blocked"}) {
		t.Fatalf("attention_changed up to the approval: %v, want [working blocked]", got)
	}
	if err := r.rt.Focus(ctx, "con_b", th.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.attention(t, th.ID); got != "blocked" {
		t.Fatalf("a focus moved a blocked thread to %q", got)
	}
	if _, err := r.rt.Cancel(ctx, th.ID); err != nil {
		t.Fatal(err)
	}
	if end := r.finished(t); end.EndState != "stopped" {
		t.Fatalf("a cancelled end: %+v", end)
	}
	if got := r.attentionEvents(th.ID); got[len(got)-1] != "idle" {
		t.Fatalf("attention_changed after a cancel: %v, want it to end with idle", got)
	}
}

// TestAFocusRacingATurnsEndLeavesNoDone: a focus that lands while the turn is writing its end
// is serialised with it, so the thread is never left `done` while a client views it.
func TestAFocusRacingATurnsEndLeavesNoDone(t *testing.T) {
	r := newRig(t, answer("hi"))
	th := r.thread(t, domain.CreateParams{})
	focused := make(chan error, 1)
	r.store.beforeFinish = func() {
		// The turn has decided nobody views the thread and is about to write. A focus that
		// were not held back until the write would mark the thread seen while it is still
		// `working`, and the write would then leave it `done`. The sleep gives the focus
		// time to get there; on a starved machine it may not, and the test then passes
		// without exercising the race rather than failing.
		go func() { focused <- r.rt.Focus(t.Context(), "con_a", th.ID) }()
		time.Sleep(100 * time.Millisecond)
	}
	r.send(t, th.ID, "hello", "")
	if err := <-focused; err != nil {
		t.Fatal(err)
	}
	if got := r.attention(t, th.ID); got != "idle" {
		t.Fatalf("a focus racing the end left %q, want idle", got)
	}
}

func (r *rig) attention(t *testing.T, threadID string) string {
	t.Helper()
	th, err := r.rt.Get(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	return th.AttentionState
}

// attentionEvents are the attention states announced for a thread, in order.
func (r *rig) attentionEvents(threadID string) []string {
	var out []string
	for _, ev := range r.bus.events() {
		if e, ok := ev.(ports.ThreadAttentionChanged); ok && e.ThreadID == threadID {
			out = append(out, e.AttentionState)
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
