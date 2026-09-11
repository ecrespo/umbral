package shellinteg

import (
	"strconv"
	"strings"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// MaxSequenceBytes bounds how much of a half-finished escape sequence the scanner will
// hold before deciding it is not a sequence at all.
//
// A command line long enough to exceed this is not a command line, and without the bound a
// stream that opens an OSC and never closes it, a truncated `cat` of a binary, would make
// the scanner buffer without limit.
const MaxSequenceBytes = 64 << 10

// scanState is where the scanner is between two calls to Scan. It has to survive across
// calls because the PTY delivers 32 KiB at a time with no regard for sequence boundaries.
type scanState int

const (
	scanGround    scanState = iota
	scanEscape              // saw ESC
	scanCSI                 // inside ESC [ …
	scanOSC                 // inside ESC ] …
	scanOSCEscape           // inside an OSC, saw ESC, expecting \ to make ST
)

// Scanner reads shell-integration markers out of a session's output stream (REQ-BLK-001,
// REQ-BLK-002, REQ-BLK-004).
//
// It lives next to the bootstrap scripts on purpose: this file and shell/*/umbral.* are
// two halves of one protocol, and a change to what a script emits is a change here.
//
// libghostty does parse OSC, but its Go bindings expose neither the semantic-prompt
// payload, the exit code in OSC 133;D, nor OSC 633 at all, so the daemon cannot get the
// command line or the exit code from the emulator. Scanning the stream ourselves is also
// what keeps the block state machine testable without a terminal.
//
// A Scanner is not safe for concurrent use; each session has its own, fed by the goroutine
// that drains its PTY.
type Scanner struct {
	state scanState
	// carried holds the part of a sequence that arrived in an earlier chunk, and is the
	// only thing the scanner copies. Everything else is emitted as a slice of the caller's
	// chunk, so ordinary coloured output crosses the scanner without an allocation.
	carried []byte
	// events is reused between calls, which is why Event.Data is only valid until the
	// next call to Scan.
	events []domain.Event
	// scratch joins carried and the in-chunk part when a sequence straddles two chunks.
	scratch []byte

	// chunk and seqStart are the current call's state: where in the caller's bytes the
	// sequence being examined begins.
	chunk    []byte
	seqStart int
}

// NewScanner returns a scanner in the ground state.
func NewScanner() *Scanner { return &Scanner{} }

// Scan splits one chunk of PTY output into ordinary byte runs and markers.
//
// The returned events, and the Data inside them, alias the input chunk and the scanner's
// own buffers: they are valid until the next call to Scan.
func (s *Scanner) Scan(chunk []byte) []domain.Event {
	s.events = s.events[:0]
	s.chunk = chunk
	// A sequence still open from the previous call continues at the first byte here.
	s.seqStart = 0

	for i := 0; i < len(chunk); i++ {
		if s.state == scanGround {
			next := indexESC(chunk[i:])
			if next < 0 {
				s.emitOutput(chunk[i:])
				break
			}
			if next > 0 {
				s.emitOutput(chunk[i : i+next])
			}
			i += next
			s.seqStart = i
			s.carried = s.carried[:0]
			s.state = scanEscape
			continue
		}
		s.step(chunk[i], i)
	}

	// Whatever is left of an unfinished sequence has to survive until the next chunk, and
	// the caller's buffer will not, so this is the one place the scanner copies.
	if s.state != scanGround && s.seqStart < len(chunk) {
		s.carried = append(s.carried, chunk[s.seqStart:]...)
	}
	s.chunk = nil
	return s.events
}

// step consumes one byte inside a sequence. i is its index in the current chunk.
func (s *Scanner) step(b byte, i int) {
	if s.length(i) > MaxSequenceBytes {
		// Something opened a sequence and never closed it. Emitting what we have keeps
		// the scanner from buffering a binary file one byte at a time.
		s.flush(i)
		return
	}

	switch s.state {
	case scanEscape:
		switch b {
		case '[':
			s.state = scanCSI
		case ']':
			s.state = scanOSC
		default:
			// Every other escape is two bytes and none of them is a marker.
			s.flush(i)
		}
	case scanCSI:
		if b >= 0x40 && b <= 0x7E {
			s.finishCSI(i)
		}
	case scanOSC:
		switch b {
		case 0x07: // BEL: what the bootstrap scripts emit
			s.finishOSC(i, 1)
		case 0x1B:
			s.state = scanOSCEscape
		}
	case scanOSCEscape:
		if b == '\\' { // ST
			s.finishOSC(i, 2)
			break
		}
		// A stray ESC inside an OSC is malformed. It is kept as payload rather than
		// treated as a new sequence: the length guard above already bounds how much
		// output a never-terminated OSC can swallow, and restarting mid-payload would
		// need a second set of boundary rules for a case no shell produces.
		s.state = scanOSC
	case scanGround:
	}
}

// length reports how long the sequence would be if it ended at chunk index i.
func (s *Scanner) length(i int) int {
	return len(s.carried) + (i + 1 - s.seqStart)
}

// bytes returns the complete sequence ending at chunk index i.
//
// In the common case the whole sequence is in this chunk and the result is a slice of it.
// Only a sequence split across two PTY reads is joined, into a buffer reused between calls.
func (s *Scanner) bytes(i int) []byte {
	inChunk := s.chunk[s.seqStart : i+1]
	if len(s.carried) == 0 {
		return inChunk
	}
	s.scratch = append(append(s.scratch[:0], s.carried...), inChunk...)
	return s.scratch
}

// flush emits the sequence ending at chunk index i as ordinary output and returns to the
// ground state. Anything the scanner does not recognise passes through byte for byte,
// which is what keeps colours and cursor movement in the stored raw output.
func (s *Scanner) flush(i int) {
	if len(s.carried) > 0 {
		// The carried prefix is emitted on its own. Two adjacent output events and one
		// longer event mean the same thing to the recorder, and this way the carried
		// buffer is never aliased by an event that outlives it.
		s.events = append(s.events, domain.Event{
			Kind: domain.EventOutput,
			Data: append([]byte(nil), s.carried...),
		})
		s.carried = s.carried[:0]
	}
	s.emitOutput(s.chunk[s.seqStart : i+1])
	s.done(i)
}

// done returns to the ground state with the next sequence starting after chunk index i.
func (s *Scanner) done(i int) {
	s.state = scanGround
	s.seqStart = i + 1
	s.carried = s.carried[:0]
}

// finishCSI handles a complete CSI. Only the alternate-screen modes mean anything here.
func (s *Scanner) finishCSI(i int) {
	seq := s.bytes(i)
	body := seq[2 : len(seq)-1]
	final := seq[len(seq)-1]

	altScreen := (final == 'h' || final == 'l') &&
		len(body) > 0 && body[0] == '?' && isAltScreenMode(string(body[1:]))

	// The two mode sequences bracket the alternate screen, and both have to land on the
	// inside of it: a stored transcript that begins by entering the alternate screen, or
	// ends by leaving one it never entered, clears the screen of whoever replays it. So
	// the enter marker goes out before its own bytes and the leave marker after, and the
	// recorder's `interactive` guard swallows both. The sequences still reach the
	// emulator, which is fed the raw chunk and not this event stream.
	if altScreen && final == 'h' {
		s.events = append(s.events, domain.Event{Kind: domain.EventAltScreen, On: true})
	}
	s.flush(i)
	if altScreen && final == 'l' {
		s.events = append(s.events, domain.Event{Kind: domain.EventAltScreen, On: false})
	}
}

// finishOSC handles a complete OSC ending at chunk index i. terminatorLen is how many
// bytes the terminator takes: one for BEL, two for ST.
func (s *Scanner) finishOSC(i, terminatorLen int) {
	seq := s.bytes(i)
	payload := string(seq[2 : len(seq)-terminatorLen])

	if event, ok := parseOSC(payload); ok {
		// A recognised marker is consumed: it is shell integration talking to the daemon,
		// not output, and keeping it would put invisible sequences in the stored
		// transcript of every command.
		s.events = append(s.events, event)
		s.done(i)
		return
	}
	s.flush(i)
}

// parseOSC recognises the sequences the bootstrap scripts emit.
func parseOSC(payload string) (domain.Event, bool) {
	code, rest, _ := strings.Cut(payload, ";")
	switch code {
	case "133":
		return parseSemanticPrompt(rest)
	case "633":
		// VS Code's dialect. The scripts use it for one thing: the command line, which
		// OSC 133 has no field for.
		if kind, cmdline, ok := strings.Cut(rest, ";"); ok && kind == "E" {
			return domain.Event{Kind: domain.EventCommandLine, Text: cmdline}, true
		}
		return domain.Event{}, false
	case "7":
		return domain.Event{Kind: domain.EventCWD, Text: parseFileURL(rest)}, true
	default:
		return domain.Event{}, false
	}
}

// parseSemanticPrompt reads the OSC 133 subcommands (API Spec §7).
func parseSemanticPrompt(rest string) (domain.Event, bool) {
	kind, params, _ := strings.Cut(rest, ";")
	switch kind {
	case "A":
		return domain.Event{Kind: domain.EventPromptStart}, true
	case "B":
		return domain.Event{Kind: domain.EventPromptEnd}, true
	case "C":
		return domain.Event{Kind: domain.EventCommandStart}, true
	case "D":
		return domain.Event{Kind: domain.EventCommandEnd, ExitCode: parseExitCode(params)}, true
	default:
		return domain.Event{}, false
	}
}

// parseExitCode reads the exit code of OSC 133;D. A shell that reports none, or reports
// something that is not a number, yields nil rather than a made-up zero: "we do not know
// how it ended" and "it succeeded" are different facts.
func parseExitCode(params string) *int {
	field, _, _ := strings.Cut(params, ";")
	if field == "" {
		return nil
	}
	code, err := strconv.Atoi(field)
	if err != nil {
		return nil
	}
	return &code
}

// parseFileURL turns the OSC 7 payload into a path, undoing the percent-encoding the
// bootstrap scripts apply to the characters that would break the URL.
func parseFileURL(payload string) string {
	path := payload
	if rest, ok := strings.CutPrefix(payload, "file://"); ok {
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			path = rest[idx:]
		} else {
			path = "/"
		}
	}
	return percentDecode(path)
}

// percentDecode expands %XX escapes. It is deliberately tolerant: a malformed escape is
// left as written rather than turned into an error, because a working directory the daemon
// cannot parse is still better recorded verbatim than dropped.
func percentDecode(path string) string {
	if !strings.ContainsRune(path, '%') {
		return path
	}
	var out strings.Builder
	out.Grow(len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == '%' && i+2 < len(path) {
			if value, err := strconv.ParseUint(path[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(value))
				i += 2
				continue
			}
		}
		out.WriteByte(path[i])
	}
	return out.String()
}

// isAltScreenMode reports whether every private mode in the list is an alternate-screen
// mode. 1049 is what every modern full-screen program uses; 47 and 1047 are the older
// forms that vi and less still emit on some systems.
func isAltScreenMode(params string) bool {
	if params == "" {
		return false
	}
	for _, mode := range strings.Split(params, ";") {
		switch mode {
		case "47", "1047", "1049":
		default:
			return false
		}
	}
	return true
}

func (s *Scanner) emitOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	s.events = append(s.events, domain.Event{Kind: domain.EventOutput, Data: data})
}

func indexESC(data []byte) int {
	for i, b := range data {
		if b == 0x1B {
			return i
		}
	}
	return -1
}
