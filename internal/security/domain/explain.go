package domain

import (
	"errors"
	"fmt"
)

// The two ways policy.explain refuses a request (API Spec §5.36). internal/api maps them to
// VALIDATION_ERROR and NOT_FOUND.
var (
	ErrInvalidExplain = errors.New("policy.explain: invalid request")
	// ErrRiskMismatch is a request whose risk contradicts the one its registered tool
	// declares; it is an ErrInvalidExplain.
	ErrRiskMismatch  = fmt.Errorf("%w: the risk is not the one the tool declares", ErrInvalidExplain)
	ErrUnknownThread = errors.New("policy.explain: unknown thread")
)

// MaxToolNameLen bounds a tool name in an explain request; a registered tool name is far
// shorter, and the bound keeps an arbitrary string out of the trace.
const MaxToolNameLen = 128

// ExplainRequest is a candidate action as policy.explain receives it (REQ-SEC-009).
type ExplainRequest struct {
	// ThreadID lends its mode, its write root and its rules; empty is no thread.
	ThreadID string
	// Mode overrides the thread's mode; required when there is no thread.
	Mode    Mode
	Tool    string
	Risk    Risk
	Target  string
	Tainted bool
}

// Validate refuses what the engine cannot evaluate.
func (r ExplainRequest) Validate() error {
	if r.Tool == "" || len(r.Tool) > MaxToolNameLen {
		return fmt.Errorf("%w: tool must be 1 to %d characters", ErrInvalidExplain, MaxToolNameLen)
	}
	switch r.Risk {
	case RiskReadOnly, RiskWriteFS, RiskExec, RiskNetwork:
	default:
		return fmt.Errorf("%w: risk %q is not one of ReadOnly, WriteFS, Exec and Network", ErrInvalidExplain, r.Risk)
	}
	switch r.Mode {
	case ModeAsk, ModeNormal, ModeAutoEdit:
	case "":
		if r.ThreadID == "" {
			return fmt.Errorf("%w: give a thread_id or a mode", ErrInvalidExplain)
		}
	default:
		return fmt.Errorf("%w: mode %q is not one of ask, normal and auto-edit", ErrInvalidExplain, r.Mode)
	}
	return nil
}
