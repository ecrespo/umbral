// Package domain holds Active AI's pure rules: which blocks earn a suggestion, what the model
// is asked, and what of its answer is kept (delta `2026-10-active-ai`, decisions 1 to 3).
package domain

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

const (
	// OutputTail is how much of a block's plain output, from its end, the model is given.
	OutputTail = 2 << 10
	// MaxSuggestion is the longest suggestion kept, in characters.
	MaxSuggestion = 200
	// MaxTokens bounds the model's answer.
	MaxTokens = 64
	// interruptExit is a shell's exit code for a command stopped by ctrl+c: the user's own doing.
	interruptExit = 130
)

// System is the fixed system prompt of every suggestion.
const System = "You help a terminal user whose command just failed. Answer with exactly one line: " +
	"the corrected command, or the likely fix in a few words. No explanation, no markdown, no quotes."

// Eligible reports whether a closed block earns a suggestion: a user's command that finished
// with an exit code other than 0 and 130.
func Eligible(b sessdomain.Block) bool {
	if b.Origin != sessdomain.OriginUser || b.State != sessdomain.BlockFinished || b.ExitCode == nil {
		return false
	}
	return *b.ExitCode != 0 && *b.ExitCode != interruptExit
}

// Prompt is the user message for a block: its command, exit code, directory and the tail of
// its output, cut at a line start.
func Prompt(b sessdomain.Block, output string) string {
	exit := 0
	if b.ExitCode != nil {
		exit = *b.ExitCode
	}
	var sb strings.Builder
	sb.WriteString("Command: " + b.Command + "\n")
	sb.WriteString("Exit code: " + strconv.Itoa(exit) + "\n")
	sb.WriteString("Directory: " + b.CWD + "\n")
	sb.WriteString("Output (last lines):\n" + Tail(output, OutputTail))
	return sb.String()
}

// Tail is the end of s within limit bytes, starting at a line start when s was cut.
func Tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[len(s)-limit:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i+1 < len(cut) {
		return cut[i+1:]
	}
	// One line longer than the limit: keep its end, from a rune boundary.
	for len(cut) > 0 && !utf8.RuneStart(cut[0]) {
		cut = cut[1:]
	}
	return cut
}

// FirstLine is the suggestion kept from an answer: its first non-empty line without control or
// format characters, backquotes trimmed, cut at MaxSuggestion characters. It is empty when the
// answer has nothing. The model read a command's output, so its text reaches every client
// already unable to move a cursor, clear a screen or reorder what is drawn.
func FirstLine(answer string) string {
	var plain sessdomain.PlainText
	plain.Write([]byte(answer))
	for line := range strings.SplitSeq(plain.String(), "\n") {
		line = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, line)
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "`"))
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > MaxSuggestion {
			line = string([]rune(line)[:MaxSuggestion])
		}
		return line
	}
	return ""
}
