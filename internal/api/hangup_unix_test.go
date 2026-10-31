//go:build unix

package api

import (
	"errors"
	"syscall"
)

// isHangUp is a write that failed because the daemon had already hung up.
func isHangUp(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}
