// Package domain is the integration surface's model: what an external reporter may say
// about a pane, and when a report is stale (REQ-INT-002, REQ-INT-003, API Spec §5.31).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The module's sentinels, which internal/api maps onto API Spec §3's codes.
var (
	ErrValidation = errors.New("integrations: validation")
	// ErrPermissionDenied is an external source reporting for a pane that hosts a thread of
	// Umbral's own agent (REQ-INT-006).
	ErrPermissionDenied = errors.New("integrations: permission denied")
)

// The bounds of API Spec §5.31 and of delta `2026-10-integration-surface`.
const (
	MaxSourceLen  = 80
	MaxAgentLen   = 80
	MaxMessageLen = 240
	// ReservedPrefix names Umbral's own sources, `umbral:agent` and `umbral:shell`: an
	// external reporter calling itself one of them would be claiming an authority it is
	// not (DD-012).
	ReservedPrefix = "umbral:"
)

// State is what an external source may report. `unknown` is not one: it is the absence of a
// report, and a source with nothing to say releases the pane instead (REQ-INT-005).
type State string

// The four states of API Spec §5.31.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateDone    State = "done"
)

// Report is one `pane.report_state`.
type Report struct {
	PaneID  string
	Source  string
	Agent   string
	State   State
	Message string
	// Seq orders a source's reports; nil when the source does not number them, and such a
	// report is never stale (REQ-INT-003).
	Seq *int64
}

// Validate checks a report against API Spec §5.31.
func (r Report) Validate() error {
	if err := ValidatePane(r.PaneID); err != nil {
		return err
	}
	if err := ValidateSource(r.Source); err != nil {
		return err
	}
	switch r.State {
	case StateIdle, StateWorking, StateBlocked, StateDone:
	default:
		return fmt.Errorf("%w: state %q is not idle, working, blocked or done", ErrValidation, r.State)
	}
	if utf8.RuneCountInString(r.Agent) > MaxAgentLen {
		return fmt.Errorf("%w: agent is longer than %d characters", ErrValidation, MaxAgentLen)
	}
	if utf8.RuneCountInString(r.Message) > MaxMessageLen {
		return fmt.Errorf("%w: message is longer than %d characters", ErrValidation, MaxMessageLen)
	}
	if r.Seq != nil && *r.Seq < 0 {
		return fmt.Errorf("%w: seq %d is negative", ErrValidation, *r.Seq)
	}
	return nil
}

// ValidatePane checks that a pane id was given; whether it names a pane is the tree's answer.
func ValidatePane(id string) error {
	if id == "" {
		return fmt.Errorf("%w: pane_id is required", ErrValidation)
	}
	return nil
}

// ValidateSource checks API Spec §5.31's `source`: 1 to 80 characters of `[A-Za-z0-9:._-]`,
// and none of Umbral's own names.
func ValidateSource(source string) error {
	if source == "" || len(source) > MaxSourceLen {
		return fmt.Errorf("%w: source must be 1 to %d characters", ErrValidation, MaxSourceLen)
	}
	for _, c := range source {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == ':' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("%w: source %q may hold only A-Z, a-z, 0-9, ':', '.', '_' and '-'",
				ErrValidation, source)
		}
	}
	if strings.HasPrefix(source, ReservedPrefix) {
		return fmt.Errorf("%w: source %q is reserved for Umbral's own detection", ErrValidation, source)
	}
	return nil
}

// Stale reports whether a report must be discarded (REQ-INT-003): it carries a seq, the
// source has an accepted report for the pane, and the seq is not past that one's. A report
// with no seq always applies, and so does the first one a source sends.
func Stale(seq *int64, last int64, exists bool) bool {
	return seq != nil && exists && *seq <= last
}
