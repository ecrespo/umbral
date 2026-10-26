package domain

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// Keys typed to a program are written as a target this way (delta `2026-10-terminal-interact`,
// decision 4): text as typed with each `<` doubled, and every other key as its name in angle
// brackets, modifiers first — `SELECT 1;<enter>`, `<ctrl+c>`, `a<<b`.

// ProgramText is text in a program-input target.
func ProgramText(s string) string { return strings.ReplaceAll(s, "<", "<<") }

// ProgramKey is a key in a program-input target.
func ProgramKey(name string) string { return "<" + name + ">" }

// programInterrupt is the key that drops the line typed so far.
const programInterrupt = "ctrl+c"

// UnknownProgramKey is a key typed to a program whose effect on its line the matcher does not
// model: history, completion, a yank, a search or a cursor move can make the line the program
// runs one the target does not show, so it asks like a destructive pattern.
const UnknownProgramKey = "unmodelled_key"

// keyEffect is what a key does to the line a program assembles, as far as the matcher reads it.
type keyEffect int

const (
	keyUnmodelled keyEffect = iota
	keyLineEnd
	keyFlush
	keyErase
	keySpace
)

// programKey is the effect of a named key. Every key a terminal sends as a carriage return or
// a line feed ends a line; ctrl+h is a backspace. What ctrl+c and ctrl+d do to the text
// before them is the program's — dropped or handed over unended, or kept — so one between
// text and more text on a line is a key the matcher does not model (keyFlush).
func programKey(name string) keyEffect {
	switch name {
	case "enter", "ctrl+m", "ctrl+j":
		return keyLineEnd
	case programInterrupt, "ctrl+d":
		return keyFlush
	case "backspace", "ctrl+h":
		return keyErase
	case "space":
		return keySpace
	}
	return keyUnmodelled
}

// ProgramLines reads a program-input target back as the lines the program receives: the text
// between line ends, with each backspace taking back the character before it and a space
// typed as one. A line after the last line end is one too, so text not yet entered is matched
// like the rest.
// A ctrl+c or ctrl+d inside a line is read both ways, the line cut there and run on, so a
// rule matching either reading matches.
func ProgramLines(target string) []string {
	lines, unmodelled := readProgram(target, false)
	if unmodelled {
		cut, _ := readProgram(target, true)
		lines = append(lines, cut...)
	}
	return lines
}

// readProgram is the target's lines, a ctrl+c or ctrl+d left out of its line or, with cut,
// ending it, and whether the target holds a key the matcher does not model: anything after a
// ctrl+c or ctrl+d with text before it on its line, but the line's end, is one.
func readProgram(target string, cut bool) (lines []string, unmodelled bool) {
	var line strings.Builder
	flushed := false
	for i := 0; i < len(target); {
		switch {
		case strings.HasPrefix(target[i:], "<<"):
			unmodelled = unmodelled || flushed
			line.WriteByte('<')
			i += 2
		case target[i] == '<':
			end := strings.IndexByte(target[i:], '>')
			if end < 0 {
				// Not something ProgramKey wrote: keep it as text, so nothing goes unmatched.
				unmodelled = unmodelled || flushed
				line.WriteString(target[i:])
				i = len(target)
				continue
			}
			switch programKey(target[i+1 : i+end]) {
			case keyFlush:
				flushed = flushed || line.Len() > 0
				if cut {
					lines = append(lines, line.String())
					line.Reset()
				}
			case keyLineEnd:
				lines = append(lines, line.String())
				line.Reset()
				flushed = false
			case keyErase:
				unmodelled = unmodelled || flushed
				s := line.String()
				if _, size := utf8.DecodeLastRuneInString(s); size > 0 {
					line.Reset()
					line.WriteString(s[:len(s)-size])
				}
			case keySpace:
				unmodelled = unmodelled || flushed
				line.WriteByte(' ')
			case keyUnmodelled:
				unmodelled = true
			}
			i += end + 1
		default:
			unmodelled = unmodelled || flushed
			line.WriteByte(target[i])
			i++
		}
	}
	if line.Len() > 0 || len(lines) == 0 {
		lines = append(lines, line.String())
	}
	return lines, unmodelled
}

// matchProgramInput is MatchDestructive for a program's input. A key the matcher does not
// model asks. Every line is matched as written against every pattern, with no shell parser and
// so no dynamic-word rule — a query's `*` is not a glob. The program may not read what it is
// sent, and the shell then reads it as its next line once the program ends (delta
// `2026-10-terminal-interact`, decision 4), so each line is also read by the shell parser: a
// pattern it finds asks, so does a script read from stdin, and a line with shell expansion
// syntax in it is decided by the parser alone, dynamic words included. A line the parser
// cannot read asks only when the shell would read on into the next one — an open quote, a
// trailing `|` or backslash — since a line it rejects runs nothing; SQL's parentheses are
// such a line. The lines are matched joined as well, as the shell would read them.
func matchProgramInput(patterns []DestructivePattern, target string) (string, bool) {
	if _, unmodelled := readProgram(target, false); unmodelled {
		return UnknownProgramKey, true
	}
	lines := ProgramLines(target)
	// The shell reads what is left as one script, a line ended by a backslash joined to the
	// next: it is matched whole as well as line by line.
	for _, line := range append(lines, strings.Join(lines, "\n")) {
		flat := strings.Join(strings.Fields(line), " ")
		for _, p := range patterns {
			if p.Expr.MatchString(flat) {
				return p.Name, true
			}
		}
		name, found := MatchDestructive(patterns, line)
		switch {
		case !found:
		case shellExpansion.MatchString(line):
			return name, true
		case name == UnknownUnparseable:
			if incompleteLine(line) {
				return name, true
			}
		case name != UnknownDynamicWord:
			return name, true
		}
	}
	return "", false
}

// incompleteLine reports whether bash would read the next line as more of this one.
func incompleteLine(line string) bool {
	if n := len(line) - len(strings.TrimRight(line, "\\")); n%2 == 1 {
		return true
	}
	_, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(line+"\n"), "")
	return syntax.IsIncomplete(err)
}

// shellExpansion is what only a shell expands in a line: a named variable, `${…}`, `$(…)`, a
// backquote. `$1` is left out, being a query's placeholder far more often than a shell's.
var shellExpansion = regexp.MustCompile("\\$[A-Za-z_{(]|`")

// ProgramTail is what of a program-input target the program has not been sent an end of
// line for: the text after its last line end, as written in the target. ctrl+c does not end
// it, since a program may keep the text before it. It is the input
// a later call adds to (REQ-FTU-003).
func ProgramTail(target string) string {
	start := 0
	for i := 0; i < len(target); {
		switch {
		case strings.HasPrefix(target[i:], "<<"):
			i += 2
		case target[i] == '<':
			end := strings.IndexByte(target[i:], '>')
			if end < 0 {
				return target[start:]
			}
			if name := target[i+1 : i+end]; programKey(name) == keyLineEnd {
				start = i + end + 1
			}
			i += end + 1
		default:
			i++
		}
	}
	return target[start:]
}
