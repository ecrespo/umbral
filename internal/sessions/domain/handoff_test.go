package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestARecorderResumesMidBlock_REQ_HND_001: a recorder exported in the middle of a block, with
// an escape sequence and a carriage return cut between two reads, and rebuilt from the JSON a
// handoff frame carries, closes that block exactly as the recorder that never stopped does.
func TestARecorderResumesMidBlock_REQ_HND_001(t *testing.T) {
	before := []Event{
		{Kind: EventCWD, Text: "/srv"},
		{Kind: EventCommandLine, Text: "make"},
		{Kind: EventCommandStart},
		output("building\r"),
		{Kind: EventAltScreen, On: true},
		{Kind: EventAltScreen, On: false},
		output("half \x1b[3"),
	}
	after := []Event{
		output("1mred\x1b[0m\r\nlast\r\n"),
		{Kind: EventCommandEnd, ExitCode: exitCode(2)},
	}

	whole := recorderFixture(t)
	whole.Claim("thr_x")
	whole.Feed(before)
	want := only(t, whole.Feed(after), ActionClose)

	first := recorderFixture(t)
	first.Claim("thr_x")
	first.Feed(before)
	raw, err := json.Marshal(first.State())
	if err != nil {
		t.Fatal(err)
	}
	var st RecorderState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	// The same clock and ids from the point the first one stopped: a fresh fixture advanced
	// past what the first recorder read.
	resumed := RecorderFrom(recorderFixture(t).cfg, st)
	for range 3 {
		resumed.cfg.Now()
	}
	got := only(t, resumed.Feed(after), ActionClose)

	if got.Plain != want.Plain {
		t.Errorf("the resumed transcript is %q, want %q", got.Plain, want.Plain)
	}
	if got.Block.ID != want.Block.ID || got.Block.Command != want.Block.Command ||
		got.Block.CWD != want.Block.CWD || got.Block.ThreadID != want.Block.ThreadID ||
		got.Block.OutputBytes != want.Block.OutputBytes || *got.Block.ExitCode != 2 {
		t.Errorf("the resumed block is %+v, want %+v", got.Block, want.Block)
	}
	if !got.Block.StartedAt.Equal(want.Block.StartedAt) {
		t.Errorf("the resumed block started at %v, want %v", got.Block.StartedAt, want.Block.StartedAt)
	}
}

// TestAPlainTextStateRoundTrips: every field of a transcript's state survives, cut inside a
// string sequence and cut after a carriage return whose meaning is not settled yet.
func TestAPlainTextStateRoundTrips(t *testing.T) {
	for _, cut := range []struct{ before, after string }{
		{"one\r\ntwo\rtw\x1b]0;tit", "le\x07o\r\n"},
		{"one\r\nprogress 10%\r", "progress 99%\r\n"},
		{"one\r\nprogress 10%\r", "\ndone\r\n"},
	} {
		var p PlainText
		p.Write([]byte(cut.before))
		st := p.State()
		q := PlainTextFrom(st)
		if !reflect.DeepEqual(q.State(), st) {
			t.Fatalf("the state did not survive: %+v, want %+v", q.State(), st)
		}
		p.Write([]byte(cut.after))
		q.Write([]byte(cut.after))
		if p.String() != q.String() {
			t.Fatalf("after %q the resumed transcript is %q, want %q", cut.before, q.String(), p.String())
		}
	}
}
