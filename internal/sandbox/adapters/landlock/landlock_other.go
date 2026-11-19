//go:build !linux

package landlock

import (
	"errors"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

func probe() domain.Status {
	return domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonUnsupportedOS}
}

// Exec is the helper's body; there is no Landlock outside Linux.
func Exec(string, []string) error { return errors.New("sandbox: Landlock is Linux's") }
