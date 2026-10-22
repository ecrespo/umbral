package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestReportValidation_REQ_INT_002(t *testing.T) {
	t.Parallel()
	seq := func(n int64) *int64 { return &n }
	ok := Report{PaneID: "w1:p1", Source: "claude-code:hook.v1_x", State: StateBlocked}
	cases := []struct {
		name   string
		change func(*Report)
		valid  bool
	}{
		{"a well-formed report", func(*Report) {}, true},
		{"with a seq, agent and message", func(r *Report) {
			r.Seq, r.Agent, r.Message = seq(3), "claude", "waiting for approval"
		}, true},
		{"no pane", func(r *Report) { r.PaneID = "" }, false},
		{"no source", func(r *Report) { r.Source = "" }, false},
		{"a source of 80", func(r *Report) { r.Source = strings.Repeat("a", 80) }, true},
		{"a source of 81", func(r *Report) { r.Source = strings.Repeat("a", 81) }, false},
		{"a space in the source", func(r *Report) { r.Source = "claude code" }, false},
		{"a slash in the source", func(r *Report) { r.Source = "claude/code" }, false},
		{"Umbral's agent as source", func(r *Report) { r.Source = "umbral:agent" }, false},
		{"Umbral's shell as source", func(r *Report) { r.Source = "umbral:shell" }, false},
		{"unknown is not reportable", func(r *Report) { r.State = "unknown" }, false},
		{"an invented state", func(r *Report) { r.State = "thinking" }, false},
		{"an agent of 81", func(r *Report) { r.Agent = strings.Repeat("é", 81) }, false},
		{"a message of 240", func(r *Report) { r.Message = strings.Repeat("é", 240) }, true},
		{"a message of 241", func(r *Report) { r.Message = strings.Repeat("é", 241) }, false},
		{"a negative seq", func(r *Report) { r.Seq = seq(-1) }, false},
	}
	for _, c := range cases {
		r := ok
		c.change(&r)
		err := r.Validate()
		if c.valid && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.valid && !errors.Is(err, ErrValidation) {
			t.Errorf("%s: got %v, want a validation error", c.name, err)
		}
	}
}

func TestStaleSeq_REQ_INT_003(t *testing.T) {
	t.Parallel()
	n := func(v int64) *int64 { return &v }
	cases := []struct {
		name   string
		seq    *int64
		last   int64
		exists bool
		stale  bool
	}{
		{"the first report, whatever its seq", n(0), 0, false, false},
		{"lower than the last", n(4), 5, true, true},
		{"equal to the last", n(5), 5, true, true},
		{"past the last", n(6), 5, true, false},
		{"no seq always applies", nil, 5, true, false},
	}
	for _, c := range cases {
		if got := Stale(c.seq, c.last, c.exists); got != c.stale {
			t.Errorf("%s: stale = %v, want %v", c.name, got, c.stale)
		}
	}
}
