package domain

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Keys typed to a program are written as a target this way (delta `2026-10-terminal-interact`,
// decision 4): text as typed with each `<` doubled, and every other key as its name in angle
// brackets, modifiers first — `SELECT 1;<enter>`, `<ctrl+c>`, `a<<b`.

// ProgramText is text in a program-input target.
func ProgramText(s string) string { return strings.ReplaceAll(s, "<", "<<") }

// ProgramKey is a key in a program-input target.
func ProgramKey(name string) string { return "<" + name + ">" }

// programEnter is the key that ends a line, and programInterrupt the one that drops it.
const (
	programEnter     = "enter"
	programInterrupt = "ctrl+c"
)

// ProgramLines reads a program-input target back as the lines the program receives: the text
// between enters, with each backspace taking back the character before it and every other key
// left out. A line after the last enter is one too, so text not yet entered is matched like
// the rest.
func ProgramLines(target string) []string {
	var lines []string
	var line strings.Builder
	for i := 0; i < len(target); {
		switch {
		case strings.HasPrefix(target[i:], "<<"):
			line.WriteByte('<')
			i += 2
		case target[i] == '<':
			end := strings.IndexByte(target[i:], '>')
			if end < 0 {
				// Not something ProgramKey wrote: keep it as text, so nothing goes unmatched.
				line.WriteString(target[i:])
				i = len(target)
				continue
			}
			switch name := target[i+1 : i+end]; name {
			case programEnter:
				lines = append(lines, line.String())
				line.Reset()
			case "backspace":
				s := line.String()
				if _, size := utf8.DecodeLastRuneInString(s); size > 0 {
					line.Reset()
					line.WriteString(s[:len(s)-size])
				}
			}
			i += end + 1
		default:
			line.WriteByte(target[i])
			i++
		}
	}
	if line.Len() > 0 || len(lines) == 0 {
		lines = append(lines, line.String())
	}
	return lines
}

// matchProgramInput is MatchDestructive for a program's input. Every line is matched as
// written against every pattern, with no shell parser and so no dynamic-word rule — a query's
// `*` is not a glob. The program may not read what it is sent, and the shell then reads it as
// its next line once the program ends (delta `2026-10-terminal-interact`, decision 4), so each
// line is also read by the shell parser: a pattern it finds asks, and a line with shell
// expansion syntax in it is decided by the parser alone, dynamic words included.
func matchProgramInput(patterns []DestructivePattern, target string) (string, bool) {
	for _, line := range ProgramLines(target) {
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
		case name != UnknownDynamicWord && name != UnknownStdinScript && name != UnknownUnparseable:
			return name, true
		}
	}
	return "", false
}

// shellExpansion is what only a shell expands in a line: a named variable, `${…}`, `$(…)`, a
// backquote. `$1` is left out, being a query's placeholder far more often than a shell's.
var shellExpansion = regexp.MustCompile("\\$[A-Za-z_{(]|`")

// ProgramTail is what of a program-input target the program has not been sent an end of
// line for: the text after its last enter or ctrl+c, as written in the target. It is the input
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
			if name := target[i+1 : i+end]; name == programEnter || name == programInterrupt {
				start = i + end + 1
			}
			i += end + 1
		default:
			i++
		}
	}
	return target[start:]
}
