package domain

import "unicode/utf8"

// plainState is where the escape-sequence stripper is between two calls to Write.
//
// It has to survive across calls because PTY output arrives in 32 KiB reads that cut
// sequences in half: an SGR that starts in one chunk and ends in the next must not leave
// half of itself in the transcript.
type plainState int

const (
	plainGround    plainState = iota
	plainEscape               // saw ESC
	plainCSI                  // inside ESC [ …, ends at 0x40-0x7E
	plainString               // inside OSC/DCS/APC/PM/SOS, ends at BEL or ST
	plainStringESC            // inside a string sequence, saw ESC, waiting for \
	plainCharset              // after ESC ( ) * +, one byte to swallow
)

// PlainText accumulates a command's output as readable text: no escape sequences, no
// control characters, carriage returns resolved (REQ-BLK-007).
//
// It is what the agent reads as context and what FTS5 indexes, so the question it answers
// is "what did this command say", not "what did the screen look like". A progress bar that
// rewrote one line two thousand times leaves one line here, because each carriage return
// discards the line drawn so far, exactly as the screen did.
//
// The zero value is ready to use and holds nothing.
type PlainText struct {
	state plainState
	// line is the current line, still subject to being erased by a carriage return.
	line []byte
	// out is everything already committed, capped at MaxOutputPlainBytes.
	out []byte
	// truncated records that the cap was reached and text was dropped.
	truncated bool
	// pendingCR records a carriage return whose meaning is not settled yet. A PTY ends
	// every line with CRLF, so a CR followed by LF is a line ending, while a CR followed
	// by anything else is a progress bar about to redraw the line.
	pendingCR bool
}

// Write feeds raw output through the stripper. It never fails and never blocks: a block
// that produces more than the cap simply stops growing.
func (p *PlainText) Write(data []byte) {
	for _, b := range data {
		p.step(b)
	}
}

func (p *PlainText) step(b byte) {
	switch p.state {
	case plainEscape:
		p.stepEscape(b)
		return
	case plainCSI:
		// A CSI ends at its final byte; everything before it is parameters.
		if b >= 0x40 && b <= 0x7E {
			p.state = plainGround
		}
		return
	case plainString:
		switch b {
		case 0x07: // BEL, the form the bootstrap scripts and most shells use
			p.state = plainGround
		case 0x1B:
			p.state = plainStringESC
		}
		return
	case plainStringESC:
		// ESC \ is ST, the other legal terminator. An ESC followed by anything else
		// inside a string sequence is malformed; treating it as the end is the reading
		// that cannot swallow the rest of the output.
		p.state = plainGround
		if b != '\\' {
			p.step(b)
		}
		return
	case plainCharset:
		p.state = plainGround
		return
	case plainGround:
	}

	if p.pendingCR {
		p.pendingCR = false
		if b != '\n' {
			// The line drawn so far is about to be overwritten on screen, so it is not
			// part of what the command said.
			p.line = p.line[:0]
		}
	}

	switch {
	case b == 0x1B:
		p.state = plainEscape
	case b == '\n':
		p.commit()
	case b == '\r':
		p.pendingCR = true
	case b == '\t':
		p.appendLine(b)
	case b == 0x08: // backspace
		p.dropLastRune()
	case b < 0x20 || b == 0x7F:
		// Every other C0 control and DEL: not text.
	default:
		p.appendLine(b)
	}
}

// appendLine adds one byte to the current line, refusing once the line alone would exceed
// the cap.
//
// The cap has to be applied here and not only when a line is committed, because output
// with no newline in it at all is ordinary: a minified file, a single-line JSON dump, a
// base64 blob. Without this the line grows without bound, past both the 1 MiB plain cap
// and the 16 MiB raw one, for as long as the command keeps writing.
func (p *PlainText) appendLine(b byte) {
	if len(p.line) >= MaxOutputPlainBytes {
		p.truncated = true
		return
	}
	p.line = append(p.line, b)
}

func (p *PlainText) stepEscape(b byte) {
	switch b {
	case '[':
		p.state = plainCSI
	case ']', 'P', '_', '^', 'X':
		p.state = plainString
	case '(', ')', '*', '+':
		p.state = plainCharset
	default:
		// A two-byte escape such as ESC = or ESC M: nothing to keep.
		p.state = plainGround
	}
}

// dropLastRune removes the last complete rune from the current line, so a backspace over
// a multi-byte character does not leave half of it behind and produce invalid UTF-8.
func (p *PlainText) dropLastRune() {
	if len(p.line) == 0 {
		return
	}
	_, size := utf8.DecodeLastRune(p.line)
	p.line = p.line[:len(p.line)-size]
}

// commit moves the current line into the committed text, respecting the cap.
func (p *PlainText) commit() {
	p.appendOut(p.line)
	p.appendOut([]byte{'\n'})
	p.line = p.line[:0]
}

func (p *PlainText) appendOut(b []byte) {
	room := MaxOutputPlainBytes - len(p.out)
	if room <= 0 {
		p.truncated = len(b) > 0 || p.truncated
		return
	}
	if len(b) > room {
		b = b[:room]
		p.truncated = true
	}
	p.out = append(p.out, b...)
}

// String reports the text accumulated so far, including a line that has not ended yet.
//
// The unfinished line is included because a command whose last line has no newline, which
// is most prompts for input and many errors, would otherwise lose it.
func (p *PlainText) String() string {
	if len(p.line) == 0 {
		return string(p.out)
	}
	tail := p.line
	if room := MaxOutputPlainBytes - len(p.out); room < len(tail) {
		if room <= 0 {
			return string(p.out)
		}
		tail = tail[:room]
	}
	return string(p.out) + string(tail)
}

// Truncated reports whether the cap discarded text.
func (p *PlainText) Truncated() bool { return p.truncated }

// Reset empties the accumulator, keeping the buffers for the next block.
func (p *PlainText) Reset() {
	p.state = plainGround
	p.line = p.line[:0]
	p.out = p.out[:0]
	p.truncated = false
	p.pendingCR = false
}
