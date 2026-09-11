package domain

import (
	"strings"
	"testing"
	"time"
)

// recorderFixture builds a recorder with a clock that advances one second per reading, so
// durations in the assertions are exact rather than approximate.
func recorderFixture(t *testing.T) *Recorder {
	t.Helper()

	next := 0
	clock := time.UnixMilli(1_757_592_000_000).UTC()
	return NewRecorder(RecorderConfig{
		SessionID: "ses_test",
		Host:      "thinkpad",
		NewID: func() string {
			next++
			return "blk_" + strings.Repeat("0", 25-len(itoa(next))) + itoa(next)
		},
		Now: func() time.Time {
			clock = clock.Add(time.Second)
			return clock
		},
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func output(text string) Event {
	return Event{Kind: EventOutput, Data: []byte(text)}
}

func exitCode(code int) *int { return &code }

// only returns the single action of the given kind, failing when there is not exactly one.
func only(t *testing.T, actions []Action, kind ActionKind) Action {
	t.Helper()

	var found []Action
	for _, action := range actions {
		if action.Kind == kind {
			found = append(found, action)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d actions of kind %d, want exactly one (all: %v)", len(found), kind, actions)
	}
	return found[0]
}

func TestBlockStartsOnOSC133C_REQ_BLK_001(t *testing.T) {
	recorder := recorderFixture(t)

	actions := recorder.Feed([]Event{
		{Kind: EventCWD, Text: "/home/u/repo"},
		{Kind: EventPromptStart},
		{Kind: EventPromptEnd},
		{Kind: EventCommandLine, Text: "go test ./..."},
		{Kind: EventCommandStart},
	})

	block := only(t, actions, ActionOpen).Block
	switch {
	case block.State != BlockRunning:
		t.Errorf("state is %q, want running", block.State)
	case block.Command != "go test ./...":
		t.Errorf("command is %q, want the one OSC 633;E announced", block.Command)
	case block.CWD != "/home/u/repo":
		t.Errorf("cwd is %q, want the one OSC 7 announced", block.CWD)
	case block.Host != "thinkpad":
		t.Errorf("host is %q", block.Host)
	case block.StartedAt.IsZero():
		t.Error("the block has no start time")
	case block.SessionID != "ses_test":
		t.Errorf("session is %q", block.SessionID)
	}
	if !recorder.Open() {
		t.Error("the recorder does not consider the block open")
	}
}

func TestBlockClosedOnOSC133D_REQ_BLK_002(t *testing.T) {
	recorder := recorderFixture(t)
	recorder.Feed([]Event{
		{Kind: EventCommandLine, Text: "false"},
		{Kind: EventCommandStart},
	})

	actions := recorder.Feed([]Event{
		output("boom\n"),
		{Kind: EventCommandEnd, ExitCode: exitCode(1)},
	})

	block := only(t, actions, ActionClose).Block
	switch {
	case block.State != BlockFinished:
		t.Errorf("state is %q, want finished", block.State)
	case block.ExitCode == nil || *block.ExitCode != 1:
		t.Errorf("exit code is %v, want 1", block.ExitCode)
	case block.EndedAt == nil:
		t.Fatal("the block has no end time")
	}

	duration := block.DurationMs()
	if duration == nil || *duration != 1000 {
		t.Errorf("duration is %v ms, want the 1000 the clock advanced", duration)
	}
	if recorder.Open() {
		t.Error("the recorder still considers the block open")
	}
}

func TestAltScreenMarksInteractive_REQ_BLK_004(t *testing.T) {
	recorder := recorderFixture(t)
	recorder.Feed([]Event{
		{Kind: EventCommandLine, Text: "vim README.md"},
		{Kind: EventCommandStart},
	})

	actions := recorder.Feed([]Event{
		output("before alt\n"),
		{Kind: EventAltScreen, On: true},
		output("\x1b[2J\x1b[HFULL SCREEN PAINT"),
	})

	if state := only(t, actions, ActionState).Block.State; state != BlockInteractive {
		t.Errorf("state is %q, want interactive", state)
	}
	// The point of REQ-BLK-004 is the exclusion, not the label: nothing painted on the
	// alternate screen may reach stored output.
	for _, action := range actions {
		if action.Kind == ActionOutput && strings.Contains(string(action.Data), "FULL SCREEN") {
			t.Fatal("alternate-screen content was offered for storage")
		}
	}

	closing := recorder.Feed([]Event{
		{Kind: EventAltScreen, On: false},
		output("after alt\n"),
		{Kind: EventCommandEnd, ExitCode: exitCode(0)},
	})
	block := only(t, closing, ActionClose)
	if strings.Contains(block.Plain, "FULL SCREEN") {
		t.Errorf("the transcript kept alternate-screen content: %q", block.Plain)
	}
	if !strings.Contains(block.Plain, "before alt") || !strings.Contains(block.Plain, "after alt") {
		t.Errorf("the transcript lost primary-screen output: %q", block.Plain)
	}
	if block.Block.State != BlockFinished {
		t.Errorf("state is %q, want finished once the command ended", block.Block.State)
	}
}

func TestPlainOutputHasNoEscapes_REQ_BLK_007(t *testing.T) {
	recorder := recorderFixture(t)
	recorder.Feed([]Event{{Kind: EventCommandStart}})

	actions := recorder.Feed([]Event{
		output("\x1b[1;32mPASS\x1b[0m\ttests\r\n"),
		output("\x1b]0;a title\x07downloading 10%\rdownloading 99%\rdone\n"),
		output("\x1b[38;2;255;0;0mred\x1b[m\n"),
		{Kind: EventCommandEnd, ExitCode: exitCode(0)},
	})

	plain := only(t, actions, ActionClose).Plain
	if strings.ContainsRune(plain, 0x1B) {
		t.Fatalf("the transcript still contains an escape: %q", plain)
	}
	want := "PASS\ttests\ndone\nred\n"
	if plain != want {
		t.Errorf("transcript is %q, want %q", plain, want)
	}
}

func TestRecorderTruncatesRawOutputAtTheCap(t *testing.T) {
	recorder := recorderFixture(t)
	recorder.Feed([]Event{{Kind: EventCommandStart}})

	// Two writes that together exceed the cap: the first fits, the second is cut.
	big := make([]byte, MaxOutputRawBytes-10)
	for i := range big {
		big[i] = 'a'
	}
	recorder.Feed([]Event{{Kind: EventOutput, Data: big}})

	actions := recorder.Feed([]Event{
		{Kind: EventOutput, Data: []byte(strings.Repeat("b", 100))},
		{Kind: EventCommandEnd, ExitCode: exitCode(0)},
	})

	stored := 0
	for _, action := range actions {
		if action.Kind == ActionOutput {
			stored += len(action.Data)
		}
	}
	if stored != 10 {
		t.Errorf("offered %d bytes past the cap, want the 10 that still fit", stored)
	}

	block := only(t, actions, ActionClose).Block
	if !block.OutputTruncated {
		t.Error("the block is not marked truncated")
	}
	// The count is what the command wrote, not what was kept.
	if want := int64(len(big) + 100); block.OutputBytes != want {
		t.Errorf("output_bytes is %d, want %d", block.OutputBytes, want)
	}
}

func TestRecorderAbandonsAnOpenBlockWhenTheSessionDies(t *testing.T) {
	recorder := recorderFixture(t)
	recorder.Feed([]Event{
		{Kind: EventCommandLine, Text: "sleep 100"},
		{Kind: EventCommandStart},
	})

	block := only(t, recorder.Abandon(), ActionClose).Block
	if block.State != BlockAbandoned {
		t.Errorf("state is %q, want abandoned", block.State)
	}
	if block.ExitCode != nil {
		t.Errorf("exit code is %v, want nil: the command never reported one", block.ExitCode)
	}
	if recorder.Abandon() != nil {
		t.Error("abandoning twice produced a second close")
	}
}

func TestRecorderAbandonsASupersededBlock_REQ_BLK_002(t *testing.T) {
	recorder := recorderFixture(t)
	recorder.Feed([]Event{{Kind: EventCommandStart}})

	actions := recorder.Feed([]Event{
		{Kind: EventCommandLine, Text: "second"},
		{Kind: EventCommandStart},
	})

	closed := only(t, actions, ActionClose).Block
	if closed.State != BlockAbandoned || closed.ExitCode != nil {
		t.Errorf("the superseded block is %q with exit %v, want abandoned with no exit code",
			closed.State, closed.ExitCode)
	}
	if opened := only(t, actions, ActionOpen).Block; opened.Command != "second" {
		t.Errorf("the new block is %q", opened.Command)
	}
}

func TestRecorderSeesShellIntegrationOnlyFromMarkers_REQ_BLK_003(t *testing.T) {
	recorder := recorderFixture(t)

	// A session full of output and full-screen programs, but no shell integration at all.
	recorder.Feed([]Event{
		output("welcome to the machine\n"),
		{Kind: EventAltScreen, On: true},
		{Kind: EventAltScreen, On: false},
	})
	if recorder.SawMarker() {
		t.Fatal("output and the alternate screen were mistaken for shell integration")
	}

	recorder.Feed([]Event{{Kind: EventPromptStart}})
	if !recorder.SawMarker() {
		t.Error("OSC 133;A did not count as shell integration")
	}
}

func TestRecorderIgnoresOutputWithNoOpenBlock(t *testing.T) {
	recorder := recorderFixture(t)

	// The banner a shell prints before its first prompt belongs to no command.
	if actions := recorder.Feed([]Event{output("Last login: today\n")}); actions != nil {
		t.Errorf("produced %v, want nothing", actions)
	}
	if actions := recorder.Feed([]Event{
		{Kind: EventCommandEnd, ExitCode: exitCode(0)},
	}); actions != nil {
		t.Errorf("a stray end marker produced %v, want nothing", actions)
	}
}
