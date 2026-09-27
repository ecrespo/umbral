package domain

// EventKind is what a scanner found in the PTY stream.
//
// The kinds mirror the sequences the bootstrap scripts in shell/ emit, plus the alternate
// screen, which no script emits and every full-screen program does. Keeping them in domain
// means the block state machine can be exercised with a hand-written event list, with no
// shell and no emulator anywhere near the test.
type EventKind int

const (
	// EventOutput carries bytes that belong to the terminal stream itself: everything the
	// scanner did not recognise as a marker. Data is only valid until the next call to
	// the scanner, so a consumer that keeps it must copy it.
	EventOutput EventKind = iota
	// EventPromptStart is OSC 133;A.
	EventPromptStart
	// EventPromptEnd is OSC 133;B: the user's input begins here.
	EventPromptEnd
	// EventCommandLine is OSC 633;E;<cmdline>, the command about to run.
	EventCommandLine
	// EventCommandStart is OSC 133;C: the command is running and a block opens
	// (REQ-BLK-001).
	EventCommandStart
	// EventCommandEnd is OSC 133;D;<exit>: the block closes with that exit code
	// (REQ-BLK-002). ExitCode is nil when the shell reported none.
	EventCommandEnd
	// EventCWD is OSC 7;file://host/path.
	EventCWD
	// EventAltScreen reports the alternate screen being entered or left (REQ-BLK-004).
	EventAltScreen
)

// Event is one item in a scanned stream: either a run of ordinary bytes or a marker.
type Event struct {
	Kind EventKind
	// Data is the byte run of an EventOutput. It aliases the scanner's input buffer.
	Data []byte
	// Text is the payload of EventCommandLine (the command) or EventCWD (the path).
	Text string
	// ExitCode is the payload of EventCommandEnd.
	ExitCode *int
	// On is the payload of EventAltScreen.
	On bool
}

// IsMarker reports whether the event came from shell integration, which is what
// REQ-BLK-003 watches for before it gives up on a session.
//
// The alternate screen is deliberately not a marker: vim takes it over just as readily in
// a shell with no integration at all, so counting it would make REQ-BLK-003 report
// integration that is not there.
func (e Event) IsMarker() bool {
	switch e.Kind {
	case EventPromptStart, EventPromptEnd, EventCommandLine, EventCommandStart,
		EventCommandEnd, EventCWD:
		return true
	default:
		return false
	}
}
