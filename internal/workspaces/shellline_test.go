package workspaces

import (
	"strings"
	"testing"
)

// The line a restored pane shows is the one place REQ-TERM-011 can be broken by a single
// character. `shellLine` renders a stored argv as text to be *typed* at a prompt, and a
// trailing newline turns that text into an instruction: the shell reads the line, runs it,
// and a restart has re-executed a command on its own — `terraform apply`, `make deploy` or
// whatever the pane happened to hold, unattended, possibly after a crash that command caused.
//
// The integration tests cannot see this. They assert that the rendered line *contains* the
// command, which a newline satisfies, and their terminals are fakes that never execute
// anything. So the property lives here, against the function that decides it.

// TestShellLineNeverEndsALine_REQ_TERM_011 is the requirement reduced to its one character.
//
// Two properties, because a line ending is dangerous in two different ways. A *trailing* one
// submits the command: the PTY's line discipline reads it as Enter and the shell runs what
// was typed, which is the failure the delta exists to prevent. One *inside* an argument is
// contained by the quoting — the submitted line has an open quote, so the shell continues at
// PS2 instead of executing — but the containment is a property of `quoteForShell`, so this
// asserts that an ordinary command carries no line ending at all and leaves the quoted case
// to `TestShellLineQuotesALineEndingIntoOneWord`.
func TestShellLineNeverEndsALine_REQ_TERM_011(t *testing.T) {
	t.Parallel()

	// Commands a person would actually store. Nothing here has a line ending in it, so
	// the rendering must have none either — an assertion that fails on any code that adds
	// one, wherever it adds it.
	ordinary := [][]string{
		{"ls"},
		{"terraform", "apply"},
		{"make", "deploy"},
		{"sh", "-c", "echo hello"},
		{"sh", "-c", "echo 'quo'te"}, // the quote that has to be re-quoted
		{"cmd", ""},                  // an empty argument still renders as a word
	}
	for _, argv := range ordinary {
		line := shellLine(argv)
		if strings.ContainsAny(line, "\n\r") {
			t.Errorf("shellLine(%q) = %q: it contains a line ending the command did not "+
				"have, so the shell would run it. REQ-TERM-011 says a stored command is "+
				"shown, never executed.", argv, line)
		}
	}

	// And nothing ever ends one, whatever the argv held. This is the property the whole
	// requirement reduces to: the text sits on the command line waiting for the user's
	// own Enter.
	withEndings := append([][]string{
		{"printf", "a\nb"},
		{"printf", "a\rb"},
		{"sh", "-c", "echo x\nrm -rf /tmp/x"},
	}, ordinary...)
	for _, argv := range withEndings {
		line := shellLine(argv)
		if line == "" {
			t.Errorf("shellLine(%q) rendered nothing", argv)
			continue
		}
		if last := line[len(line)-1]; last == '\n' || last == '\r' {
			t.Errorf("shellLine(%q) = %q: it ends a line, so the shell submits it. "+
				"REQ-TERM-011 forbids a restart running a stored command on its own.",
				argv, line)
		}
	}
}

// TestShellLineQuotesALineEndingIntoOneWord is the other half of the same property.
//
// A newline may legitimately appear *inside* an argument — `printf 'a\nb'` is an ordinary
// command — and the rendering has to keep it from reaching the shell as a line ending. Single
// quotes do that: the shell sees an unterminated word and waits at PS2 rather than executing.
// Asserting the quoting, and not merely the absence of a newline, is what stops a future
// "fix" that strips the character and silently changes the user's command.
func TestShellLineQuotesALineEndingIntoOneWord(t *testing.T) {
	t.Parallel()

	line := shellLine([]string{"printf", "a\nb"})
	const want = "printf 'a\\nb'"
	if line == want {
		t.Fatalf("shellLine escaped the newline into %q; it must be carried literally "+
			"inside the quotes, not rewritten", line)
	}
	if !strings.HasPrefix(line, "printf '") || !strings.HasSuffix(line, "'") {
		t.Errorf("shellLine(printf a\\nb) = %q, want the argument single-quoted whole", line)
	}
}

// TestShellLineRendersWhatTheUserWouldType keeps the text readable, which is the other thing
// REQ-TERM-011 asks for: the command is "left visible in the pane" for a person to read,
// decide on and press Enter. Quoting every word would satisfy the safety property and defeat
// the purpose.
func TestShellLineRendersWhatTheUserWouldType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"ls", "-la", "/tmp"}, "ls -la /tmp"},
		{[]string{"git", "commit", "-m", "fix"}, "git commit -m fix"},
		{[]string{"sh", "-c", "echo hi"}, "sh -c 'echo hi'"},
		{[]string{"echo", "it's"}, `echo 'it'\''s'`},
		{[]string{"cmd", ""}, "cmd ''"},
	} {
		if got := shellLine(tc.argv); got != tc.want {
			t.Errorf("shellLine(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}
