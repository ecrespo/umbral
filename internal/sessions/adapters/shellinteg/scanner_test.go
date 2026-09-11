package shellinteg

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// collected is what a scanner produced, flattened so a test can assert on it without
// worrying that Event.Data is only valid until the next call.
type collected struct {
	events []domain.Event
	output []byte
}

// scanAll feeds the stream in chunks of the given size, which is how the boundary cases
// are reached: a marker split across two PTY reads must still be one marker.
func scanAll(t *testing.T, stream string, chunkSize int) collected {
	t.Helper()

	scanner := NewScanner()
	var got collected
	data := []byte(stream)
	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))
		for _, event := range scanner.Scan(data[i:end]) {
			if event.Kind == domain.EventOutput {
				got.output = append(got.output, event.Data...)
			}
			copied := event
			copied.Data = nil
			got.events = append(got.events, copied)
		}
	}
	return got
}

// kinds lists the non-output events, which is what the marker assertions care about.
func (c collected) kinds() []domain.EventKind {
	var out []domain.EventKind
	for _, event := range c.events {
		if event.Kind != domain.EventOutput {
			out = append(out, event.Kind)
		}
	}
	return out
}

func TestScannerReadsACompleteCommandCycle(t *testing.T) {
	const stream = "\x1b]7;file://host/home/u/my%20repo\x07" +
		"\x1b]133;A\x07prompt$ \x1b]133;B\x07" +
		"\x1b]633;E;go test ./...\x07\x1b]133;C\x07" +
		"ok\tgithub.com/x\t0.2s\r\n" +
		"\x1b]133;D;1\x07"

	// Every chunk size from one byte up exercises a different set of split points, and a
	// marker that only parses when it arrives whole is the bug this is looking for.
	for _, size := range []int{1, 3, 7, 64, len(stream)} {
		got := scanAll(t, stream, size)

		want := []domain.EventKind{
			domain.EventCWD, domain.EventPromptStart, domain.EventPromptEnd,
			domain.EventCommandLine, domain.EventCommandStart, domain.EventCommandEnd,
		}
		if kinds := got.kinds(); !equalKinds(kinds, want) {
			t.Fatalf("chunk size %d: markers are %v, want %v", size, kinds, want)
		}

		const wantOutput = "prompt$ ok\tgithub.com/x\t0.2s\r\n"
		if string(got.output) != wantOutput {
			t.Errorf("chunk size %d: output is %q, want %q", size, got.output, wantOutput)
		}

		byKind := map[domain.EventKind]domain.Event{}
		for _, event := range got.events {
			byKind[event.Kind] = event
		}
		if cwd := byKind[domain.EventCWD].Text; cwd != "/home/u/my repo" {
			t.Errorf("chunk size %d: cwd is %q, want the percent-decoded path", size, cwd)
		}
		if cmd := byKind[domain.EventCommandLine].Text; cmd != "go test ./..." {
			t.Errorf("chunk size %d: command is %q", size, cmd)
		}
		exit := byKind[domain.EventCommandEnd].ExitCode
		if exit == nil || *exit != 1 {
			t.Errorf("chunk size %d: exit code is %v, want 1", size, exit)
		}
	}
}

func TestScannerPassesOrdinarySequencesThrough(t *testing.T) {
	// Colour, cursor movement and a title: none of them is shell integration, and all of
	// them belong in the raw output a block stores for faithful re-rendering.
	const stream = "\x1b[1;32mgreen\x1b[0m\x1b[2K\r\x1b]0;my title\x07plain"

	for _, size := range []int{1, 2, 5, len(stream)} {
		got := scanAll(t, stream, size)
		if len(got.kinds()) != 0 {
			t.Errorf("chunk size %d: reported markers %v, want none", size, got.kinds())
		}
		if string(got.output) != stream {
			t.Errorf("chunk size %d: output is %q, want the stream unchanged", size, got.output)
		}
	}
}

func TestScannerDetectsTheAlternateScreen(t *testing.T) {
	const stream = "before\x1b[?1049h\x1b[?47hfull screen\x1b[?1049lafter"

	got := scanAll(t, stream, 4)
	want := []domain.EventKind{domain.EventAltScreen, domain.EventAltScreen, domain.EventAltScreen}
	if kinds := got.kinds(); !equalKinds(kinds, want) {
		t.Fatalf("markers are %v, want three alt-screen events", kinds)
	}

	var states []bool
	for _, event := range got.events {
		if event.Kind == domain.EventAltScreen {
			states = append(states, event.On)
		}
	}
	if len(states) != 3 || !states[0] || !states[1] || states[2] {
		t.Errorf("alt-screen states are %v, want on, on, off", states)
	}
	// The mode sequences stay in the stream: the emulator is fed the raw chunk and has to
	// switch screens too.
	if string(got.output) != stream {
		t.Errorf("output is %q, want the stream unchanged", got.output)
	}
}

func TestScannerBracketsTheAlternateScreen_REQ_BLK_004(t *testing.T) {
	// The enter marker must precede its own bytes and the leave marker must follow its
	// own, so that a recorder excluding alternate-screen output excludes both mode
	// sequences. A stored transcript carrying an unpaired 1049 clears the screen of
	// whoever replays it.
	const stream = "a\x1b[?1049hb\x1b[?1049lc"

	for _, size := range []int{1, 2, 3, 5, len(stream)} {
		scanner := NewScanner()
		var order []string
		data := []byte(stream)
		for i := 0; i < len(data); i += size {
			for _, event := range scanner.Scan(data[i:min(i+size, len(data))]) {
				switch {
				case event.Kind == domain.EventAltScreen && event.On:
					order = append(order, "on")
				case event.Kind == domain.EventAltScreen:
					order = append(order, "off")
				default:
					order = append(order, string(event.Data))
				}
			}
		}

		joined := strings.Join(order, "|")
		// "on" comes before the \x1b[?1049h run; "off" comes after the \x1b[?1049l run.
		onIndex := strings.Index(joined, "on")
		enterIndex := strings.Index(joined, "\x1b[?1049h")
		offIndex := strings.Index(joined, "off")
		leaveIndex := strings.Index(joined, "\x1b[?1049l")
		if onIndex < 0 || offIndex < 0 {
			t.Fatalf("chunk size %d: events are %q, want both markers", size, joined)
		}
		if enterIndex >= 0 && onIndex > enterIndex {
			t.Errorf("chunk size %d: the enter sequence is emitted before its marker: %q", size, joined)
		}
		if leaveIndex >= 0 && offIndex < leaveIndex {
			t.Errorf("chunk size %d: the leave sequence is emitted after its marker: %q", size, joined)
		}
	}
}

func TestScannerIgnoresNonAltScreenModes(t *testing.T) {
	// Mouse tracking and bracketed paste are private modes as well, and a block that went
	// `interactive` because a program enabled the mouse would stop recording its output.
	const stream = "\x1b[?1000h\x1b[?2004h\x1b[?25l"

	if got := scanAll(t, stream, 3); len(got.kinds()) != 0 {
		t.Errorf("reported %v, want no markers", got.kinds())
	}
}

func TestScannerBoundsAnUnterminatedSequence(t *testing.T) {
	// A `cat` of a binary can open an OSC that never ends. The scanner must give up and
	// emit the bytes rather than buffer the rest of the session.
	stream := "\x1b]133;" + strings.Repeat("x", MaxSequenceBytes+100)

	got := scanAll(t, stream, 4096)
	if len(got.kinds()) != 0 {
		t.Errorf("reported %v, want no markers", got.kinds())
	}
	if !bytes.Contains(got.output, []byte(strings.Repeat("x", 1000))) {
		t.Error("the buffered bytes were never emitted as output")
	}
}

func TestScannerAcceptsTheStringTerminator(t *testing.T) {
	// zsh and some frameworks use ESC \ instead of BEL.
	got := scanAll(t, "\x1b]133;C\x1b\\done", 1)
	if kinds := got.kinds(); !equalKinds(kinds, []domain.EventKind{domain.EventCommandStart}) {
		t.Fatalf("markers are %v, want one command start", kinds)
	}
	if string(got.output) != "done" {
		t.Errorf("output is %q, want %q", got.output, "done")
	}
}

func TestScannerReportsNoExitCodeWhenTheShellGivesNone(t *testing.T) {
	got := scanAll(t, "\x1b]133;D\x07", 1)
	if len(got.events) != 1 || got.events[0].ExitCode != nil {
		t.Errorf("exit code is %v, want nil: not knowing is not the same as zero", got.events)
	}
}

func equalKinds(got, want []domain.EventKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
