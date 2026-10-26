package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// Bounds on one terminal_interact send_keys (delta `2026-10-terminal-interact`, decision 2).
const (
	MaxKeys     = 64
	MaxKeysText = 4096
)

// KeyNames are the keys terminal_interact can name, besides a letter or a digit with ctrl or
// alt. The emulator's adapter encodes each one.
var KeyNames = []string{
	"enter", "tab", "backspace", "escape", "space",
	"up", "down", "left", "right", "home", "end", "page_up", "page_down", "insert", "delete",
	"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12",
}

var keyNames = func() map[string]bool {
	m := make(map[string]bool, len(KeyNames))
	for _, n := range KeyNames {
		m[n] = true
	}
	return m
}()

// Key is one entry of send_keys: a printable text typed as it is, or a named key with its
// modifiers, which the emulator encodes.
type Key struct {
	Text  string
	Name  string
	Ctrl  bool
	Alt   bool
	Shift bool
}

// ErrTakenOver is a call on a thread's PTY while the user holds it (REQ-FTU-002).
var ErrTakenOver = errors.New("taken_over: the user has taken over this terminal")

// ErrNoTerminal is a read of a thread's PTY the thread has not started: only send_keys
// starts one (decision 3).
var ErrNoTerminal = errors.New("no_terminal: this thread has no terminal yet; send_keys starts one")

// ErrTerminalChanged is a send whose decision was made on a terminal state that no longer
// holds (decision 5).
var ErrTerminalChanged = errors.New("the terminal changed since the call was decided; read the screen and try again")

// IsCharKey reports whether name is a letter or a digit, which a key may name with ctrl or alt.
func IsCharKey(name string) bool {
	return len(name) == 1 && (name[0] >= 'a' && name[0] <= 'z' || name[0] >= '0' && name[0] <= '9')
}

// ValidateKeys checks a send_keys list (decision 2).
func ValidateKeys(keys []Key) error {
	if len(keys) == 0 || len(keys) > MaxKeys {
		return fmt.Errorf("%w: send between 1 and %d keys", ErrValidation, MaxKeys)
	}
	total := 0
	for i, k := range keys {
		switch {
		case k.Text != "" && (k.Name != "" || k.Ctrl || k.Alt || k.Shift):
			return fmt.Errorf("%w: key %d has a text and a key; a text takes no modifiers", ErrValidation, i)
		case k.Text != "":
			if err := validText(k.Text); err != nil {
				return fmt.Errorf("%w: key %d: %w", ErrValidation, i, err)
			}
			total += len(k.Text)
		case keyNames[k.Name]:
		case IsCharKey(k.Name) && (k.Ctrl || k.Alt):
		case IsCharKey(k.Name):
			return fmt.Errorf("%w: key %d: send a letter or a digit as text, or with ctrl or alt", ErrValidation, i)
		default:
			return fmt.Errorf("%w: key %d: %q is not a key", ErrValidation, i, k.Name)
		}
	}
	if total > MaxKeysText {
		return fmt.Errorf("%w: at most %d bytes of text per call", ErrValidation, MaxKeysText)
	}
	return nil
}

// validText refuses what would reach the terminal as anything but typed characters.
func validText(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("the text is not UTF-8")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("the text holds the control character %U; send it as a key", r)
		}
	}
	return nil
}

// InteractState is how keys sent to a thread's PTY would be read, taken when the call is
// decided and checked again before it is written (decision 5).
type InteractState struct {
	// AtPrompt is the shell at its prompt: integration `osc133`, no block open, no agent
	// command in flight. Keys then go to its line editor.
	AtPrompt bool
	// Line is what the agent has typed at the prompt since the last enter or ctrl+c.
	Line string
	// Program is what the agent has typed to the program in the foreground since its last
	// enter or ctrl+c there, as a program-input target (RenderKeys): the start of the line the
	// next enter is decided on (REQ-FTU-003).
	Program string
	// TakenOver is the user holding the terminal (REQ-FTU-002).
	TakenOver bool
}

// Plan is what a send_keys does to the prompt's line.
type Plan struct {
	// Submit is an enter at the prompt: Line is then the line the shell receives.
	Submit bool
	// Line is the prompt's line after the keys, or the submitted one. It is empty for keys to
	// a program.
	Line string
	// Interrupt is a ctrl+c at the prompt, which empties the line.
	Interrupt bool
	// Target is keys to a program as the policy decides them: the input since its last
	// enter or ctrl+c, these keys included. Program is what of it a later call adds to.
	Target  string
	Program string
}

// PlanKeys reads keys against the state they are sent in (decision 4). At the prompt only
// text, backspace and a final enter or ctrl+c are accepted; elsewhere the keys are a program's
// input and every key goes.
func PlanKeys(state InteractState, keys []Key) (Plan, error) {
	if err := ValidateKeys(keys); err != nil {
		return Plan{}, err
	}
	if !state.AtPrompt {
		target := state.Program + RenderKeys(keys)
		return Plan{Target: target, Program: secdomain.ProgramTail(target)}, nil
	}
	line := []rune(state.Line)
	for i, k := range keys {
		isCtrlC := k.Name == "c" && k.Ctrl && !k.Alt && !k.Shift
		switch {
		case k.Text != "":
			line = append(line, []rune(k.Text)...)
		case isCtrlC:
			// The kernel flushes what follows an interrupt in the same write, and the shell may
			// drop the first byte of its next line: ctrl+c ends the call too.
			if i != len(keys)-1 {
				return Plan{}, fmt.Errorf("%w: at the shell's prompt, ctrl+c must be the last key; send what follows in another call", ErrValidation)
			}
			return Plan{Interrupt: true}, nil
		case k.Name == "backspace" && !k.Ctrl && !k.Alt:
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		case k.Name == "enter" && !k.Ctrl && !k.Alt && !k.Shift:
			if i != len(keys)-1 {
				return Plan{}, fmt.Errorf("%w: at the shell's prompt, enter must be the last key; send what follows in another call", ErrValidation)
			}
			return Plan{Submit: true, Line: string(line)}, nil
		default:
			return Plan{}, fmt.Errorf("%w: at the shell's prompt send only text, backspace, ctrl+c and a final enter; type a command as text", ErrValidation)
		}
	}
	return Plan{Line: string(line)}, nil
}

// RenderKeys writes keys as a program-input target: `SELECT 1;<enter>`, `<ctrl+c>`.
func RenderKeys(keys []Key) string {
	var b strings.Builder
	for _, k := range keys {
		if k.Text != "" {
			b.WriteString(secdomain.ProgramText(k.Text))
			continue
		}
		name := k.Name
		if k.Shift {
			name = "shift+" + name
		}
		if k.Alt {
			name = "alt+" + name
		}
		if k.Ctrl {
			name = "ctrl+" + name
		}
		b.WriteString(secdomain.ProgramKey(name))
	}
	return b.String()
}

// Bounds on read_screen and wait_for (decision 2).
const (
	MaxScreenText   = 64 << 10
	MinWaitIdle     = 100 * time.Millisecond
	MaxWaitIdle     = 10 * time.Second
	DefaultWaitTime = 10 * time.Second
	MaxWaitTime     = 120 * time.Second
)

// Screen is a thread's PTY as read_screen shows it: the last MaxScreenText bytes of the screen's
// plain text, the cursor's cell, the size, and whether the shell is at its prompt.
type Screen struct {
	Text     string
	CursorX  uint16
	CursorY  uint16
	Size     Size
	AtPrompt bool
}

// WaitCondition is what wait_for waits for: exactly one of a regex over the screen's plain
// text, a stretch with no output, or the foreground program's end.
type WaitCondition struct {
	Regex   *regexp.Regexp
	Idle    time.Duration
	Exit    bool
	Timeout time.Duration
}

// Validate checks that exactly one condition is set and the durations are in range.
func (c WaitCondition) Validate() error {
	set := 0
	if c.Regex != nil {
		set++
	}
	if c.Idle != 0 {
		set++
		if c.Idle < MinWaitIdle || c.Idle > MaxWaitIdle {
			return fmt.Errorf("%w: idle_ms must be between %d and %d", ErrValidation, MinWaitIdle.Milliseconds(), MaxWaitIdle.Milliseconds())
		}
	}
	if c.Exit {
		set++
	}
	if set != 1 {
		return fmt.Errorf("%w: wait_for takes exactly one of regex, idle_ms and exit", ErrValidation)
	}
	if c.Timeout <= 0 || c.Timeout > MaxWaitTime {
		return fmt.Errorf("%w: timeout_ms must be between 1 and %d", ErrValidation, MaxWaitTime.Milliseconds())
	}
	return nil
}

// What ended a wait_for.
const (
	WaitMatched = "matched"
	WaitIdle    = "idle"
	WaitExited  = "exited"
	WaitTimeout = "timeout"
)

// WaitOutcome is a wait_for's end and the screen when it ended.
type WaitOutcome struct {
	Reason string
	Screen Screen
}

// TailText cuts a screen's text to its last MaxScreenText bytes, at a rune boundary.
func TailText(s string) string {
	if len(s) <= MaxScreenText {
		return s
	}
	cut := len(s) - MaxScreenText
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
