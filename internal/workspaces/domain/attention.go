package domain

// The sources a pane's attention state can come from, besides an external integration's own
// `source` (API §4's `state_source`).
const (
	// SourceAgent is Umbral's own agent, for a pane whose live session is one of its threads'.
	SourceAgent = "umbral:agent"
	// SourceShell is Umbral's derivation from the block lifecycle (DD-012, delta
	// `2026-10-integration-surface`).
	SourceShell = "umbral:shell"
)

// StateInputs is everything that decides a pane's attention state.
type StateInputs struct {
	// OwnerThreadID is the thread whose PTY the pane's live session is, "" for none, and
	// ThreadAttention that thread's attention state.
	OwnerThreadID   string
	ThreadAttention AttentionState
	// ReportSource and ReportState are the latest report of an external source that has not
	// released the pane (REQ-INT-002, REQ-INT-005); ReportSource is "" when there is none.
	ReportSource string
	ReportState  AttentionState
	// Live says the pane has a live session. Integration is that session's OSC 133 verdict,
	// and BlockState the state of its latest block, "" when it has none.
	Live        bool
	Integration string
	BlockState  string
}

// ResolveAttention applies DD-012: exactly one source decides, in this order — Umbral's own
// agent, which nothing displaces (REQ-INT-006); an external report (REQ-INT-002); then the
// block lifecycle, `umbral:shell`. A command running, or a full-screen program, is `working`;
// a finished or abandoned one, or a shell at a prompt that has run nothing yet, is `idle`. A
// session that never spoke OSC 133 gives the lifecycle nothing to read, and a pane with no
// live session has no lifecycle at all: both are `unknown`, with no source.
func ResolveAttention(in StateInputs) (AttentionState, string) {
	switch {
	case in.OwnerThreadID != "":
		return in.ThreadAttention, SourceAgent
	case in.ReportSource != "":
		return in.ReportState, in.ReportSource
	case !in.Live:
		return AttentionUnknown, ""
	}
	switch in.BlockState {
	case "running", "interactive":
		return AttentionWorking, SourceShell
	case "finished", "abandoned":
		return AttentionIdle, SourceShell
	case "":
		if in.Integration == "osc133" {
			return AttentionIdle, SourceShell
		}
	}
	return AttentionUnknown, ""
}
