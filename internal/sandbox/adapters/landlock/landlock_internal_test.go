package landlock

import (
	"errors"
	"testing"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// TestANetworkProfileIsRefusedWhereItCannotBeEnforced_REQ_SBX_008: a profile that names a
// network is refused where the kernel has no network rules, and where nothing is enforced at
// all — it is never run without its network. The same profile without one goes through as
// before: confined where the sandbox enforces, as it came where it does not.
func TestANetworkProfileIsRefusedWhereItCannotBeEnforced_REQ_SBX_008(t *testing.T) {
	net := domain.Profile{Write: []string{"/w"}, Net: &domain.Net{Connect: []uint16{4096}}}
	plain := domain.Profile{Write: []string{"/w"}}
	for name, c := range map[string]struct {
		status   domain.Status
		wantArgs int
	}{
		"a kernel without network rules": {domain.Status{State: domain.StateEnforcing, Mechanism: Mechanism}, 5},
		"no sandbox at all":              {domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonNoLandlock}, 1},
		"the opt-out":                    {domain.Status{State: domain.StateOff}, 1},
		// Network without enforcing is not a state the probe gives; it enforces nothing.
		"a status that contradicts itself": {domain.Status{State: domain.StateUnavailable, Network: true}, 1},
	} {
		s := &Sandbox{helper: []string{"/helper"}, status: c.status}
		if argv, err := s.Command(net, []string{"true"}); !errors.Is(err, domain.ErrNoNetwork) {
			t.Errorf("%s: a profile with a network gave %v, %v; want ErrNoNetwork", name, argv, err)
		}
		if argv, err := s.Command(plain, []string{"true"}); err != nil || len(argv) != c.wantArgs {
			t.Errorf("%s: a profile without a network gave %v, %v", name, argv, err)
		}
	}
	ok := &Sandbox{helper: []string{"/helper"}, status: domain.Status{State: domain.StateEnforcing, Mechanism: Mechanism, Network: true}}
	if argv, err := ok.Command(net, []string{"true"}); err != nil || len(argv) != 5 {
		t.Errorf("where the network is enforced the profile gave %v, %v", argv, err)
	}
}
