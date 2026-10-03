// Package domain is the waits module's pure core: what a wait may target, how an observed
// thread settles a wait pinned to one turn (DD-011, REQ-AUT-001), and how a pane's output is
// read line by line against an RE2 expression (REQ-AUT-003). It imports nothing but the
// standard library and the sessions domain's escape-sequence stripper.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// Errors the module returns; internal/api maps them to the wire.
var (
	ErrValidation = errors.New("invalid wait")
	// ErrTimeout is a wait that reached its deadline (REQ-AUT-004). It is always wrapped
	// in a *TimeoutError, which carries what was last observed.
	ErrTimeout = errors.New("the wait timed out")
)

// TimeoutError is a wait that reached its deadline, with the last state it observed: a
// thread's state for thread.wait, the last line evaluated for block.wait_output. Nothing is
// resent or retried (REQ-AUT-004).
type TimeoutError struct {
	LastState string
	Waited    time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("the wait timed out after %v; last observed: %q", e.Waited, e.LastState)
}

func (e *TimeoutError) Unwrap() error { return ErrTimeout }

// Bounds of every wait (API §8): there is no wait without a deadline.
const (
	MinTimeout = time.Second
	MaxTimeout = time.Hour
)

// ParseTimeout checks timeout_ms against API §8's bounds.
func ParseTimeout(ms int64) (time.Duration, error) {
	d := time.Duration(ms) * time.Millisecond
	if d < MinTimeout || d > MaxTimeout {
		return 0, fmt.Errorf("%w: timeout_ms must be %d-%d", ErrValidation, MinTimeout.Milliseconds(), MaxTimeout.Milliseconds())
	}
	return d, nil
}

// State is what a thread wait observes: the thread's attention state, or `stopped` (API
// §4's Thread, §5.20's `until`). `working` is observed and reported but cannot be a target.
type State string

// The states a thread wait observes; all but working can be targets.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateDone    State = "done"
	StateStopped State = "stopped"
	// StateUnknown answers for a pinned turn a later one replaced before the wait learned
	// how it ended (API §4's attention_state has the same value).
	StateUnknown State = "unknown"
)

// Targets is a wait's `until`.
type Targets map[State]bool

// Has reports whether s is a target.
func (t Targets) Has(s State) bool { return t[s] }

// ParseTargets checks `until`: at least one of idle, done, blocked and stopped.
func ParseTargets(until []string) (Targets, error) {
	if len(until) == 0 {
		return nil, fmt.Errorf("%w: until needs at least one state", ErrValidation)
	}
	ts := Targets{}
	for _, u := range until {
		switch s := State(u); s {
		case StateIdle, StateDone, StateBlocked, StateStopped:
			ts[s] = true
		default:
			return nil, fmt.Errorf("%w: until %q is not idle, done, blocked or stopped", ErrValidation, u)
		}
	}
	return ts, nil
}

// Observed is one reading of a thread: what a wait sees, the thread's latest turn, and
// whether that turn is still running.
type Observed struct {
	State   State
	TurnID  string
	Running bool
}

// Observe maps a thread's state and attention state to what a wait sees. A stopped thread
// is `stopped` whatever its attention says; otherwise the attention state is the answer.
func Observe(threadState, attention, turnID string) Observed {
	o := Observed{State: State(attention), TurnID: turnID}
	switch threadState {
	case "running", "awaiting_approval":
		o.Running = true
	case "stopped":
		o.State = StateStopped
	}
	return o
}

// Tracker settles one wait pinned to one turn (DD-011).
type Tracker struct {
	pinned  string
	targets Targets
	last    State
	// ended records that a reading of the pinned turn showed it no longer running.
	ended bool
	// replaced records a reading of a later turn before the pinned turn's end was known.
	replaced bool
}

// NewTracker pins turnID; "" is a thread that never had a turn.
func NewTracker(turnID string, targets Targets) *Tracker {
	return &Tracker{pinned: turnID, targets: targets}
}

// Pinned is the turn the wait is pinned to.
func (t *Tracker) Pinned() string { return t.pinned }

// Last is the last state observed for the pinned turn.
func (t *Tracker) Last() State { return t.last }

// Replaced reports that a later turn was read before the pinned turn's end was known: the
// wait then needs that end from the turn's own event or from the runtime.
func (t *Tracker) Replaced() bool { return t.replaced }

// Observe takes one reading and reports whether the wait has settled, and with which state.
//
//   - The pinned turn in a target state settles it.
//   - Once the pinned turn has ended, the only change left without a new turn is a client
//     viewing it, `done` to `idle`; when that cannot reach a target the wait settles at
//     once with the state the turn ended in, rather than running to its deadline. A turn
//     cancelled into `stopped` settles this way when `stopped` is not a target (delta
//     `2026-10-thread-cancel`, decision 7).
//   - A reading of a later turn is never the pinned turn's state (REQ-AUT-001). When the
//     pinned turn's end was already read it settles with that end; otherwise the wait is
//     Replaced and waits for End.
func (t *Tracker) Observe(o Observed) (State, bool) {
	if o.TurnID != t.pinned {
		if t.ended {
			return t.last, true
		}
		t.replaced = true
		return "", false
	}
	t.last, t.ended = o.State, !o.Running
	if t.targets.Has(o.State) {
		return o.State, true
	}
	if t.ended && (o.State != StateDone || !t.targets.Has(StateIdle)) {
		return o.State, true
	}
	return "", false
}

// End takes the pinned turn's end as its own event or the runtime reports it. Once a later
// turn has replaced it, viewing the thread can no longer move it to `idle`, so its end
// settles the wait whatever the targets.
func (t *Tracker) End(s State) (State, bool) {
	if t.replaced {
		t.last, t.ended = s, true
		return s, true
	}
	return t.Observe(Observed{State: s, TurnID: t.pinned})
}

// Bounds of block.wait_output's `lines` (API §5.30).
const (
	DefaultLines = 200
	MaxLines     = 2000
)

// OutputParams is block.wait_output's input.
type OutputParams struct {
	SessionID string
	BlockID   string
	Regex     string
	Lines     int
	TimeoutMS int64
}

// Validate checks the parameters and fills the default `lines`.
func (p OutputParams) Validate() (OutputParams, error) {
	if (p.SessionID == "") == (p.BlockID == "") {
		return p, fmt.Errorf("%w: give exactly one of session_id and block_id", ErrValidation)
	}
	if p.Regex == "" {
		return p, fmt.Errorf("%w: regex is required", ErrValidation)
	}
	if p.Lines == 0 {
		p.Lines = DefaultLines
	}
	if p.Lines < 1 || p.Lines > MaxLines {
		return p, fmt.Errorf("%w: lines must be 1-%d", ErrValidation, MaxLines)
	}
	if _, err := ParseTimeout(p.TimeoutMS); err != nil {
		return p, err
	}
	return p, nil
}

// Tail is the last n lines of a screen's text, without the blank lines below the last one
// written.
func Tail(text string, n int) []string {
	lines := strings.Split(strings.TrimRight(text, "\n \t"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// Match is a matched line and its number, counted from the first line the wait evaluated.
type Match struct {
	Line   string
	Number int
}

// Matcher evaluates lines in order against one RE2 expression.
type Matcher struct {
	re   *regexp.Regexp
	n    int
	last string
}

// NewMatcher compiles expr; Go's regexp is RE2, so no expression can backtrack without
// bound.
func NewMatcher(expr string) (*Matcher, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: regex: %w", ErrValidation, err)
	}
	return &Matcher{re: re}, nil
}

// Lines evaluates complete lines, numbering each, and returns the first that matches.
func (m *Matcher) Lines(lines []string) (Match, bool) {
	for _, l := range lines {
		m.n++
		m.last = l
		if m.re.MatchString(l) {
			return Match{Line: l, Number: m.n}, true
		}
	}
	return Match{}, false
}

// Partial evaluates the line still being written — a prompt waiting for input — under the
// number it will have, without consuming it.
func (m *Matcher) Partial(line string) (Match, bool) {
	if line == "" {
		return Match{}, false
	}
	m.last = line
	if m.re.MatchString(line) {
		return Match{Line: line, Number: m.n + 1}, true
	}
	return Match{}, false
}

// Restart numbers the next line 1 again: a re-read screen is a new window (API §5.30).
func (m *Matcher) Restart() { m.n = 0 }

// Last is the last line evaluated, what a timeout reports.
func (m *Matcher) Last() string { return m.last }

// Feed turns a pane's raw output into lines, through the same stripper blocks use. It takes
// the committed lines out of the stripper on every write, so a wait can follow output of any
// length.
type Feed struct {
	plain sessdomain.PlainText
}

// Seed starts the feed inside a line already on screen, so what follows continues it.
func (f *Feed) Seed(line string) { f.plain.Write([]byte(line)) }

// Write feeds a chunk and returns the lines it completed and the line still open.
func (f *Feed) Write(chunk []byte) (lines []string, partial string) {
	f.plain.Write(chunk)
	if committed := f.plain.TakeCommitted(); committed != "" {
		lines = strings.Split(strings.TrimSuffix(committed, "\n"), "\n")
	}
	return lines, f.plain.Current()
}
