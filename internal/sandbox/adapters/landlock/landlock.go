// Package landlock confines a process with Linux's Landlock (delta `2026-11-sandbox-linux`).
//
// The restriction is taken by the process that becomes the confined command: the daemon runs
// its own binary with HelperFlag, which applies the profile to itself and then execs the
// command (Exec). Landlock needs no privilege and no user namespace.
package landlock

import (
	"errors"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// HelperFlag is the first argument of the helper's command line:
// `<helper> --sandbox-exec <profile> -- <command> [args]`.
const HelperFlag = "--sandbox-exec"

// Mechanism is what Status names while enforcing.
const Mechanism = "landlock"

// Sandbox is the Landlock adapter of ports.Sandbox.
type Sandbox struct {
	helper []string
	status domain.Status
}

// New returns the adapter. helper is the command that reaches Exec — the daemon's own
// executable — and is run with HelperFlag, the profile, `--` and the command. The kernel is
// asked once, here, whether it has Landlock.
func New(helper ...string) *Sandbox {
	s := &Sandbox{helper: helper, status: probe()}
	if len(helper) == 0 && s.status.Enforcing() {
		s.status = domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonNoHelper}
	}
	return s
}

// Status implements ports.Sandbox.
func (s *Sandbox) Status() domain.Status { return s.status }

// Command implements ports.Sandbox.
func (s *Sandbox) Command(p domain.Profile, argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, errors.New("sandbox: no command to confine")
	}
	if p.Net != nil && (!s.status.Enforcing() || !s.status.Network) {
		// A network is never dropped from a profile in silence, where the kernel cannot
		// enforce one or where nothing is enforced at all: the caller asks Status first, and
		// says what it finds.
		return nil, domain.ErrNoNetwork
	}
	if !s.status.Enforcing() {
		return argv, nil
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	arg, err := p.Encode()
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), s.helper...)
	out = append(out, HelperFlag, arg, "--")
	return append(out, argv...), nil
}
