//go:build !darwin

package seatbelt

import "github.com/ecrespo/umbral/internal/sandbox/domain"

// probe: sandbox-exec is macOS's.
func probe() domain.Status {
	return domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonUnsupportedOS}
}
