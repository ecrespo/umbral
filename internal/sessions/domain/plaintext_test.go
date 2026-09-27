package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlainTextStopsAtTheCap(t *testing.T) {
	var plain PlainText

	// Ten lines past the cap, each ending in a newline so they are committed rather than
	// held as an unfinished line.
	line := strings.Repeat("x", 1023) + "\n"
	for written := 0; written < MaxOutputPlainBytes+10*len(line); written += len(line) {
		plain.Write([]byte(line))
	}

	if got := len(plain.String()); got != MaxOutputPlainBytes {
		t.Errorf("the transcript is %d bytes, want it capped at %d", got, MaxOutputPlainBytes)
	}
	if !plain.Truncated() {
		t.Error("the transcript is not marked truncated")
	}
}

func TestPlainTextKeepsAnUnfinishedLine(t *testing.T) {
	// A prompt for input, or an error with no trailing newline, is the last thing a
	// command said and the most useful thing in the transcript.
	var plain PlainText
	plain.Write([]byte("done\nEnter password: "))

	if got := plain.String(); got != "done\nEnter password: " {
		t.Errorf("transcript is %q, want the unfinished line kept", got)
	}
}

func TestPlainTextResolvesBackspace(t *testing.T) {
	var plain PlainText
	// The backspace lands on a two-byte rune on purpose. Removing one byte of it would
	// leave invalid UTF-8 in a column FTS5 indexes and the agent reads.
	plain.Write([]byte("naï\bive\n"))

	if got := plain.String(); got != "naive\n" {
		t.Errorf("transcript is %q, want %q", got, "naive\n")
	}
	if !utf8.ValidString(plain.String()) {
		t.Error("the transcript is not valid UTF-8")
	}
}

func TestPlainTextCapsALineWithNoNewline_REQ_BLK_007(t *testing.T) {
	// A minified file, a one-line JSON dump or a base64 blob has no newline in it, so a
	// cap applied only when a line is committed is a cap that never applies.
	var plain PlainText
	blob := strings.Repeat("z", 64<<10)
	for written := 0; written < 4*MaxOutputPlainBytes; written += len(blob) {
		plain.Write([]byte(blob))
	}

	if got := len(plain.String()); got > MaxOutputPlainBytes {
		t.Errorf("the transcript grew to %d bytes with no newline, want it capped at %d",
			got, MaxOutputPlainBytes)
	}
	if !plain.Truncated() {
		t.Error("the transcript is not marked truncated")
	}
}
