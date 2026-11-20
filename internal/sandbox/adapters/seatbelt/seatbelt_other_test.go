//go:build !darwin

package seatbelt

import (
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// TestSeatbeltIsMacOSOnly_REQ_SBX_003: on any other system the adapter says it cannot confine
// and why, and returns a command as it was.
func TestSeatbeltIsMacOSOnly_REQ_SBX_003(t *testing.T) {
	s := New()
	if st := s.Status(); st.State != domain.StateUnavailable || st.Reason != domain.ReasonUnsupportedOS || st.Mechanism != "" {
		t.Errorf("Status = %+v", st)
	}
	if argv, err := s.Command(agent, []string{"/bin/agent"}); err != nil || !slices.Equal(argv, []string{"/bin/agent"}) {
		t.Errorf("Command = %q, %v", argv, err)
	}
}
