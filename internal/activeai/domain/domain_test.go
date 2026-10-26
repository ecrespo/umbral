package domain

import (
	"strings"
	"testing"
)

// TestTheOutputTailStartsAtALine_REQ_AAI_001: the model is given the end of the output from a
// line start, and the first non-empty line of its answer, trimmed and cut (decisions 2 and 3).
func TestTheOutputTailStartsAtALine_REQ_AAI_001(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 3000) + "\nlast line\n"
	if got := Tail(long, OutputTail); got != "last line\n" {
		t.Errorf("Tail = %q", got)
	}
	if got := Tail("short\n", OutputTail); got != "short\n" {
		t.Errorf("Tail kept %q", got)
	}
	if got := Tail(strings.Repeat("é", 2000), OutputTail); !strings.HasPrefix(got, "é") {
		t.Errorf("Tail cut a rune: %q", got[:4])
	}
	if got := FirstLine(strings.Repeat("a", 300)); len(got) != MaxSuggestion {
		t.Errorf("FirstLine kept %d characters", len(got))
	}
	if got := FirstLine("\n  ` `\n``ls -la``\nmore"); got != "ls -la" {
		t.Errorf("FirstLine = %q", got)
	}
	// What the model writes reaches clients without control or format characters.
	if got := FirstLine("\x1b[2J\x1b]0;x\x07git\u202e status\x07"); got != "git status" {
		t.Errorf("FirstLine kept control or format characters: %q", got)
	}
}
