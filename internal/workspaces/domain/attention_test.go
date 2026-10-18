package domain

import "testing"

// TestOneAuthorityPerPane_DD_012: a pane's attention state comes from exactly one source,
// in DD-012's order — Umbral's own agent, then an external report, then the block lifecycle —
// and is `unknown` with no source when none of them says anything.
func TestOneAuthorityPerPane_DD_012(t *testing.T) {
	t.Parallel()
	live := StateInputs{Live: true, Integration: "osc133"}
	cases := []struct {
		name       string
		in         StateInputs
		want       AttentionState
		wantSource string
	}{
		{"no session", StateInputs{}, AttentionUnknown, ""},
		{"a shell at its first prompt", live, AttentionIdle, SourceShell},
		{"a command running", with(live, func(in *StateInputs) { in.BlockState = "running" }), AttentionWorking, SourceShell},
		{"a full-screen program", with(live, func(in *StateInputs) { in.BlockState = "interactive" }), AttentionWorking, SourceShell},
		{"a command finished", with(live, func(in *StateInputs) { in.BlockState = "finished" }), AttentionIdle, SourceShell},
		{"a command abandoned", with(live, func(in *StateInputs) { in.BlockState = "abandoned" }), AttentionIdle, SourceShell},
		{"no integration and no blocks", StateInputs{Live: true, Integration: "none"}, AttentionUnknown, ""},
		{"integration still pending", StateInputs{Live: true, Integration: "pending"}, AttentionUnknown, ""},
		{"a session that exited", StateInputs{Integration: "osc133", BlockState: "finished"}, AttentionUnknown, ""},
		{
			"an external report beats the shell",
			with(live, func(in *StateInputs) {
				in.BlockState, in.ReportSource, in.ReportState = "running", "claude-hook", AttentionBlocked
			}),
			AttentionBlocked, "claude-hook",
		},
		{
			"an external report on a pane with no session",
			StateInputs{ReportSource: "claude-hook", ReportState: AttentionDone},
			AttentionDone, "claude-hook",
		},
		{
			"the own agent beats a report",
			with(live, func(in *StateInputs) {
				in.OwnerThreadID, in.ThreadAttention = "thr_1", AttentionWorking
				in.ReportSource, in.ReportState = "claude-hook", AttentionBlocked
			}),
			AttentionWorking, SourceAgent,
		},
	}
	for _, c := range cases {
		state, source := ResolveAttention(c.in)
		if state != c.want || source != c.wantSource {
			t.Errorf("%s: (%s, %q), want (%s, %q)", c.name, state, source, c.want, c.wantSource)
		}
	}
}

func with(in StateInputs, change func(*StateInputs)) StateInputs {
	change(&in)
	return in
}
