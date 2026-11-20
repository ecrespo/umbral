package seatbelt

import (
	"context"
	"os/exec"
	"time"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// probe runs the tool once under a profile that allows everything. It fails where the tool is
// missing and where the daemon is itself inside a sandbox, which cannot apply another.
func probe() domain.Status {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- a fixed path and fixed arguments
	if err := exec.CommandContext(ctx, Tool, "-p", "(version 1)(allow default)", "/usr/bin/true").Run(); err != nil {
		return domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonNoSeatbelt}
	}
	return domain.Status{State: domain.StateEnforcing, Mechanism: Mechanism, Network: true}
}
