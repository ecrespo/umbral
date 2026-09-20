package domain

import "time"

// AttentionState is what a pane or a thread reports about itself, and what a workspace
// rolls up (REQ-WS-006).
type AttentionState string

// The five states of API Spec §4, ordered here as REQ-WS-006 ranks them.
const (
	AttentionBlocked AttentionState = "blocked"
	AttentionWorking AttentionState = "working"
	AttentionDone    AttentionState = "done"
	AttentionIdle    AttentionState = "idle"
	// AttentionUnknown is the absence of a report, not a report of nothing. It is the
	// state every pane starts in and the only one F0 produces: `pane_state_reports` is
	// migration 0004 and threads are F1, so nothing reports yet (delta
	// `2026-09-pane-attention-state`).
	AttentionUnknown AttentionState = "unknown"
)

// urgency ranks the states as REQ-WS-006 writes them: blocked > working > done > idle >
// unknown. Lower is more urgent.
//
// The requirement's closing clause — "`unknown` propagates only when every child is
// `unknown`" — needs no separate rule here: it follows from unknown being last, because any
// other child outranks it. It is written into the criterion to pin the reading, and it is
// tested for the same reason.
var urgency = map[AttentionState]int{
	AttentionBlocked: 0,
	AttentionWorking: 1,
	AttentionDone:    2,
	AttentionIdle:    3,
	AttentionUnknown: 4,
}

// Valid reports whether a state is one of the five.
func (s AttentionState) Valid() bool {
	_, ok := urgency[s]
	return ok
}

// Rollup is REQ-WS-006: the most urgent state among the children, or `idle` when there are
// none.
//
// Empty is `idle` and not `unknown`, which is the requirement's own distinction and worth
// keeping straight: an empty workspace is quiet, a workspace of panes nobody reports on is
// unobserved. Rendering the second as the first would tell the user everything is fine at
// exactly the moment an integration went silent.
//
// A child carrying a state this build does not recognise is ranked as `unknown`, which is
// what it is: a value nothing here knows the urgency of. Note that skipping such a child
// instead would give the same answer, because the accumulator starts at `unknown` — the
// mapping is explicit anyway, so that the rule survives someone changing that starting
// value. What must never happen is the other direction: an unrecognised state ranking as
// urgent, which would make every workspace containing one shout.
func Rollup(children []AttentionState) AttentionState {
	if len(children) == 0 {
		return AttentionIdle
	}
	best := AttentionUnknown
	for _, child := range children {
		if !child.Valid() {
			child = AttentionUnknown
		}
		if urgency[child] < urgency[best] {
			best = child
		}
	}
	return best
}

// Workspace is the top of the tree (API Spec §4 `Workspace`).
type Workspace struct {
	ID          string
	Label       string
	CWD         string
	OrderIndex  int
	RollupState AttentionState
	TabIDs      []string
	CreatedAt   time.Time
	ClosedAt    *time.Time
}

// Tab groups panes inside a workspace (API Spec §4 `Tab`).
type Tab struct {
	ID            string
	WorkspaceID   string
	Label         string
	OrderIndex    int
	FocusedPaneID string
	CreatedAt     time.Time
	ClosedAt      *time.Time
}

// Pane is one addressable slot, hosting at most one live session (API Spec §4 `Pane`).
type Pane struct {
	ID          string
	TabID       string
	WorkspaceID string
	SessionID   string
	ThreadID    string
	Label       string
	CWD         string
	Command     []string
	Env         map[string]string
	OrderIndex  int
	// Aliases are the identifiers this pane answered to before it was moved, oldest first,
	// and they stay resolvable for the life of the terminal (REQ-WS-007). The current id is
	// included, so a client can render the whole set without appending it.
	Aliases []string
	// AttentionState is `unknown` in F0 and StateSource is empty with it: nothing reports
	// a pane state until migration 0004 brings `pane_state_reports`.
	AttentionState AttentionState
	StateSource    string
	CreatedAt      time.Time
	ClosedAt       *time.Time
}

// CreateWorkspaceParams is what `workspace.create` takes (API Spec §5.4).
type CreateWorkspaceParams struct {
	CWD      string
	Label    string
	TabLabel string
	Focus    bool
}

// Tree is what `workspace.create` returns: the three records in one response, which is
// REQ-WS-001's whole point — a client never has to make three calls to reach a usable
// workspace, and never sees a half-built one.
type Tree struct {
	Workspace Workspace
	Tab       Tab
	RootPane  Pane
}

// SplitDirection is where `pane.split` puts the new pane (API Spec §5.6).
type SplitDirection string

// The two directions API Spec §5.6 defines.
const (
	SplitRight SplitDirection = "right"
	SplitDown  SplitDirection = "down"
)

// Valid reports whether the direction is one the API defines.
func (d SplitDirection) Valid() bool { return d == SplitRight || d == SplitDown }

// Split ratio bounds from API Spec §5.6: `ratio?: 0.1-0.9 (default 0.5)`.
const (
	MinSplitRatio     = 0.1
	MaxSplitRatio     = 0.9
	DefaultSplitRatio = 0.5
)

// SplitParams is what `pane.split` takes.
type SplitParams struct {
	PaneID    string
	Direction SplitDirection
	Ratio     float64
	CWD       string
	Command   []string
	Env       map[string]string
	Focus     bool
}

// MoveDestinationType is where `pane.move` sends a pane (API Spec §5.6).
type MoveDestinationType string

// The three destinations API Spec §5.6 defines for `pane.move`.
const (
	MoveToTab          MoveDestinationType = "tab"
	MoveToNewTab       MoveDestinationType = "new_tab"
	MoveToNewWorkspace MoveDestinationType = "new_workspace"
)

// Valid reports whether the destination type is one the API defines.
func (t MoveDestinationType) Valid() bool {
	return t == MoveToTab || t == MoveToNewTab || t == MoveToNewWorkspace
}

// MoveDestination names where a pane is going.
type MoveDestination struct {
	Type MoveDestinationType
	// TabID is required for `tab`, and WorkspaceID for `new_tab`. `new_workspace` needs
	// neither.
	TabID       string
	WorkspaceID string
	Label       string
}

// MoveParams is what `pane.move` takes.
type MoveParams struct {
	PaneID      string
	Destination MoveDestination
}
