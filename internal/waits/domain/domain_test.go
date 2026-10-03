package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func targets(t *testing.T, until ...string) Targets {
	t.Helper()
	ts, err := ParseTargets(until)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestParseTargetsRefusesWhatAWaitCannotObserve(t *testing.T) {
	for _, until := range [][]string{nil, {}, {"working"}, {"running"}, {"done", ""}} {
		if _, err := ParseTargets(until); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseTargets(%q) = %v, want ErrValidation", until, err)
		}
	}
	ts := targets(t, "idle", "done", "blocked", "stopped", "done")
	for _, s := range []State{StateIdle, StateDone, StateBlocked, StateStopped} {
		if !ts.Has(s) {
			t.Errorf("targets lack %s", s)
		}
	}
}

func TestTimeoutIsBoundedFromOneSecondToOneHour(t *testing.T) {
	for _, ms := range []int64{0, 999, 3_600_001, -1} {
		if _, err := ParseTimeout(ms); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseTimeout(%d) = %v, want ErrValidation", ms, err)
		}
	}
	if d, err := ParseTimeout(1000); err != nil || d != time.Second {
		t.Errorf("ParseTimeout(1000) = %v %v", d, err)
	}
	if d, err := ParseTimeout(3_600_000); err != nil || d != time.Hour {
		t.Errorf("ParseTimeout(3600000) = %v %v", d, err)
	}
}

func TestObserveMapsThreadAndAttention(t *testing.T) {
	cases := []struct {
		state, attention string
		want             State
		running          bool
	}{
		{"running", "working", StateWorking, true},
		{"awaiting_approval", "blocked", StateBlocked, true},
		{"idle", "done", StateDone, false},
		{"idle", "idle", StateIdle, false},
		{"stopped", "idle", StateStopped, false},
	}
	for _, c := range cases {
		o := Observe(c.state, c.attention, "trn_1")
		if o.State != c.want || o.Running != c.running || o.TurnID != "trn_1" {
			t.Errorf("Observe(%s, %s) = %+v", c.state, c.attention, o)
		}
	}
}

// TestATargetReachedByThePinnedTurnSettles_REQ_AUT_001: the pinned turn reaching a target
// settles the wait with that state.
func TestATargetReachedByThePinnedTurnSettles_REQ_AUT_001(t *testing.T) {
	tr := NewTracker("trn_a", targets(t, "done"))
	if _, ok := tr.Observe(Observed{State: StateWorking, TurnID: "trn_a", Running: true}); ok {
		t.Fatal("settled while working")
	}
	if s, ok := tr.Observe(Observed{State: StateDone, TurnID: "trn_a"}); !ok || s != StateDone {
		t.Fatalf("got %s %v, want done", s, ok)
	}
}

// TestALaterTurnNeverSatisfiesThePinnedWait_REQ_AUT_001: a turn after the pinned one ends
// the wait with the pinned turn's last state, never with the new turn's.
func TestALaterTurnNeverSatisfiesThePinnedWait_REQ_AUT_001(t *testing.T) {
	tr := NewTracker("trn_a", targets(t, "blocked", "idle"))
	tr.Observe(Observed{State: StateWorking, TurnID: "trn_a", Running: true})
	tr.Observe(Observed{State: StateDone, TurnID: "trn_a"}) // idle is still reachable
	if s, ok := tr.Observe(Observed{State: StateBlocked, TurnID: "trn_b", Running: true}); !ok || s != StateDone {
		t.Fatalf("got %s %v, want the pinned turn's done", s, ok)
	}
}

// TestAnEndedTurnThatCannotReachATargetSettles_REQ_AUT_001: once the pinned turn has ended
// and no target is reachable without a new turn, the wait returns its end state at once —
// `stopped` after a cancel (delta 2026-10-thread-cancel, decision 7), or `done` when `idle`
// is not a target.
func TestAnEndedTurnThatCannotReachATargetSettles_REQ_AUT_001(t *testing.T) {
	cases := []struct {
		until []string
		end   State
		ok    bool
	}{
		{[]string{"done"}, StateStopped, true},
		{[]string{"blocked"}, StateDone, true},
		{[]string{"blocked", "idle"}, StateDone, false}, // a client viewing it makes it idle
		{[]string{"blocked"}, StateIdle, true},
	}
	for _, c := range cases {
		tr := NewTracker("trn_a", targets(t, c.until...))
		s, ok := tr.Observe(Observed{State: c.end, TurnID: "trn_a"})
		if ok != c.ok || (ok && s != c.end) {
			t.Errorf("until %v, end %s: got %s %v", c.until, c.end, s, ok)
		}
	}
}

func TestAThreadWithNoTurnSettlesAtOnce(t *testing.T) {
	tr := NewTracker("", targets(t, "done"))
	if s, ok := tr.Observe(Observed{State: StateIdle}); !ok || s != StateIdle {
		t.Fatalf("got %s %v", s, ok)
	}
}

func TestTailKeepsTheLastLinesWithoutTrailingBlanks(t *testing.T) {
	got := Tail("a\nb\nc\n\n\n", 2)
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("Tail = %q", got)
	}
	if got := Tail("one", 200); len(got) != 1 || got[0] != "one" {
		t.Fatalf("Tail = %q", got)
	}
	if got := Tail("\n\n", 5); len(got) != 0 {
		t.Fatalf("Tail = %q", got)
	}
}

func TestMatcherNumbersLinesAndMatchesAPartialLineWithoutConsumingIt(t *testing.T) {
	m, err := NewMatcher(`pass(word)?:`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lines([]string{"one", "two"}); ok {
		t.Fatal("matched nothing that matches")
	}
	if _, ok := m.Partial("Enter pass"); ok {
		t.Fatal("a partial line matched early")
	}
	match, ok := m.Partial("Enter password:")
	if !ok || match.Line != "Enter password:" || match.Number != 3 {
		t.Fatalf("partial = %+v %v", match, ok)
	}
	if m.Last() != "Enter password:" {
		t.Errorf("Last = %q", m.Last())
	}
	match, ok = m.Lines([]string{"x", "pass:"})
	if !ok || match.Number != 4 || match.Line != "pass:" {
		t.Fatalf("lines = %+v %v", match, ok)
	}
	if _, err := NewMatcher(`(?<name>x)(`); !errors.Is(err, ErrValidation) {
		t.Errorf("a bad expression = %v, want ErrValidation", err)
	}
}

func TestFeedSplitsCompleteLinesAndKeepsTheUnfinishedOne(t *testing.T) {
	var f Feed
	lines, partial := f.Write([]byte("\x1b[1mhello\x1b[0m\r\nwor"))
	if len(lines) != 1 || lines[0] != "hello" || partial != "wor" {
		t.Fatalf("lines %q partial %q", lines, partial)
	}
	lines, partial = f.Write([]byte("ld\r\n$ "))
	if len(lines) != 1 || lines[0] != "world" || partial != "$ " {
		t.Fatalf("lines %q partial %q", lines, partial)
	}
}

func TestOutputParamsNeedExactlyOneTarget(t *testing.T) {
	ok := OutputParams{SessionID: "ses_1", Regex: "x", TimeoutMS: 1000}
	if _, err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	p, _ := ok.Validate()
	if p.Lines != DefaultLines {
		t.Errorf("lines default = %d", p.Lines)
	}
	for _, bad := range []OutputParams{
		{Regex: "x", TimeoutMS: 1000},
		{SessionID: "s", BlockID: "b", Regex: "x", TimeoutMS: 1000},
		{SessionID: "s", Regex: "", TimeoutMS: 1000},
		{SessionID: "s", Regex: "x", TimeoutMS: 1000, Lines: 2001},
		{SessionID: "s", Regex: "x", TimeoutMS: 10},
	} {
		if _, err := bad.Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("%+v: %v, want ErrValidation", bad, err)
		}
	}
}

// TestFeedKeepsReadingPastTheStrippersCap_REQ_AUT_003: output in chunks that never end on a
// newline, well past the stripper's 1 MiB transcript cap, still reaches the matcher.
func TestFeedKeepsReadingPastTheStrippersCap_REQ_AUT_003(t *testing.T) {
	var f Feed
	m, _ := NewMatcher(`^NEEDLE$`)
	chunk := []byte(strings.Repeat("x", 99) + "\n" + "partial-")
	for written := 0; written < 3<<20; written += len(chunk) {
		lines, partial := f.Write(chunk)
		if _, ok := m.Lines(lines); ok {
			t.Fatal("matched filler")
		}
		_, _ = m.Partial(partial)
	}
	lines, _ := f.Write([]byte("\nNEEDLE\n"))
	if _, ok := m.Lines(lines); !ok {
		t.Fatalf("the needle after 3 MiB was never evaluated: %q", lines)
	}
}

// TestALaterTurnSeenFirstDoesNotAnswerForThePinnedOne_REQ_AUT_001: when the first reading is
// already of a later turn, its state is not the pinned turn's; the wait needs the pinned
// turn's own end, and once it has it, that end settles the wait.
func TestALaterTurnSeenFirstDoesNotAnswerForThePinnedOne_REQ_AUT_001(t *testing.T) {
	tr := NewTracker("trn_a", targets(t, "idle"))
	if s, ok := tr.Observe(Observed{State: StateDone, TurnID: "trn_b"}); ok {
		t.Fatalf("settled with the later turn's %s", s)
	}
	if !tr.Replaced() {
		t.Fatal("the later turn was not noted")
	}
	// Replaced, the pinned turn can no longer be viewed into idle: its end settles.
	if s, ok := tr.End(StateDone); !ok || s != StateDone {
		t.Fatalf("End = %s %v", s, ok)
	}

	tr = NewTracker("trn_a", targets(t, "done"))
	tr.Observe(Observed{State: StateWorking, TurnID: "trn_a", Running: true})
	if _, ok := tr.Observe(Observed{State: StateWorking, TurnID: "trn_b", Running: true}); ok {
		t.Fatal("a later turn answered `working` for the pinned one")
	}
	if s, ok := tr.End(StateStopped); !ok || s != StateStopped {
		t.Fatalf("End = %s %v", s, ok)
	}
}
