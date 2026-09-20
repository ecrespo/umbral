package domain

import (
	"slices"
	"testing"
)

// TestRollupPrefersBlocked_REQ_WS_006 walks REQ-WS-006's whole sentence, including the two
// clauses that API Spec §4's four-value type list used to obscure: all-`unknown` children
// roll up to `unknown`, and a workspace with no children is `idle` rather than `unknown`.
//
// That distinction is the reason this is not a one-line test. `idle` means nothing is
// happening; `unknown` means nobody has said what is happening. A rollup that collapsed the
// second into the first would report calm at the moment an integration went silent.
func TestRollupPrefersBlocked_REQ_WS_006(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		children []AttentionState
		want     AttentionState
	}{
		{"blocked outranks everything", []AttentionState{
			AttentionIdle, AttentionDone, AttentionWorking, AttentionBlocked,
		}, AttentionBlocked},
		{"blocked outranks everything, whatever the order", []AttentionState{
			AttentionBlocked, AttentionWorking, AttentionDone, AttentionIdle,
		}, AttentionBlocked},
		{"working outranks done and idle", []AttentionState{
			AttentionIdle, AttentionWorking, AttentionDone,
		}, AttentionWorking},
		{"done outranks idle", []AttentionState{
			AttentionIdle, AttentionDone,
		}, AttentionDone},
		{"idle outranks unknown", []AttentionState{
			AttentionUnknown, AttentionIdle,
		}, AttentionIdle},
		{"unknown propagates only when every child is unknown", []AttentionState{
			AttentionUnknown, AttentionUnknown, AttentionUnknown,
		}, AttentionUnknown},
		{"one known child is enough to stop unknown propagating", []AttentionState{
			AttentionUnknown, AttentionUnknown, AttentionIdle,
		}, AttentionIdle},
		{"a workspace with no children is idle, not unknown", nil, AttentionIdle},
		{"an empty slice is the same as none", []AttentionState{}, AttentionIdle},
		// These two pin the direction that matters. Ranking an unrecognised state as
		// `unknown` and skipping it outright give the same answer — the accumulator starts
		// at `unknown` — so no test can tell those apart. What a test can catch is the
		// dangerous reading: an unrecognised state ranking as urgent, which would make
		// every workspace holding one shout.
		{"a list of only unrecognised states is unknown", []AttentionState{
			AttentionState("hibernating"), AttentionState("frozen"),
		}, AttentionUnknown},
		{"an unrecognised state never outranks a known one", []AttentionState{
			AttentionState("hibernating"), AttentionIdle,
		}, AttentionIdle},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Rollup(tc.children); got != tc.want {
				t.Errorf("Rollup(%v) = %q, want %q", tc.children, got, tc.want)
			}
		})
	}
}

// TestRollupOrderMatchesTheRequirement pins the ordering itself rather than samples of it,
// so a future edit that swaps two states fails here and not in whichever test happened to
// cover that pair.
func TestRollupOrderMatchesTheRequirement(t *testing.T) {
	t.Parallel()

	// REQ-WS-006, in its own words: blocked > working > done > idle > unknown.
	order := []AttentionState{
		AttentionBlocked, AttentionWorking, AttentionDone, AttentionIdle, AttentionUnknown,
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			pair := []AttentionState{order[j], order[i]}
			if got := Rollup(pair); got != order[i] {
				t.Errorf("Rollup(%v) = %q, want %q: %q must outrank %q",
					pair, got, order[i], order[i], order[j])
			}
		}
	}

	// Every state in the ordering is one the type accepts, and there are no others. A sixth
	// state added to the type without a rank would sort as unknown and nobody would notice.
	for _, s := range order {
		if !s.Valid() {
			t.Errorf("%q is in the requirement's ordering but Valid() rejects it", s)
		}
	}
	if len(urgency) != len(order) {
		t.Errorf("urgency ranks %d states, the requirement names %d", len(urgency), len(order))
	}
}

// TestSplitDefaultsMatchTheApiSpec guards the three numbers §5.6 writes down, because they
// are the kind of constant that gets "tidied" into a round number.
func TestSplitDefaultsMatchTheApiSpec(t *testing.T) {
	t.Parallel()

	if MinSplitRatio != 0.1 || MaxSplitRatio != 0.9 || DefaultSplitRatio != 0.5 {
		t.Errorf("split ratio bounds are %v-%v default %v, API Spec §5.6 says 0.1-0.9 default 0.5",
			MinSplitRatio, MaxSplitRatio, DefaultSplitRatio)
	}
	for _, d := range []SplitDirection{SplitRight, SplitDown} {
		if !d.Valid() {
			t.Errorf("%q is a direction §5.6 defines but Valid() rejects it", d)
		}
	}
	if SplitDirection("left").Valid() {
		t.Error(`"left" is not a direction §5.6 defines, and Valid() accepted it`)
	}
	for _, m := range []MoveDestinationType{MoveToTab, MoveToNewTab, MoveToNewWorkspace} {
		if !m.Valid() {
			t.Errorf("%q is a destination §5.6 defines but Valid() rejects it", m)
		}
	}
	if MoveDestinationType("window").Valid() {
		t.Error(`"window" is not a destination §5.6 defines, and Valid() accepted it`)
	}
}

// TestRollupDoesNotMutateItsInput: the service passes a slice it built from live panes, and
// a Rollup that sorted in place would reorder them under the caller.
func TestRollupDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	children := []AttentionState{AttentionIdle, AttentionBlocked, AttentionWorking}
	before := slices.Clone(children)
	Rollup(children)
	if !slices.Equal(children, before) {
		t.Errorf("Rollup reordered its argument: %v, was %v", children, before)
	}
}
