//go:build !darwin

package seatbelt

import (
	"errors"
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

// TestANetworkProfileIsRefusedWhereNothingIsEnforced_REQ_SBX_008: where the adapter confines
// nothing, a profile without a network runs as it came; one that names a network is refused,
// never run without it.
func TestANetworkProfileIsRefusedWhereNothingIsEnforced_REQ_SBX_008(t *testing.T) {
	s := New()
	p := domain.Profile{Net: &domain.Net{Connect: []uint16{4096}}}
	if argv, err := s.Command(p, []string{"true"}); !errors.Is(err, domain.ErrNoNetwork) {
		t.Errorf("a profile with a network gave %v, %v; want ErrNoNetwork", argv, err)
	}
}
