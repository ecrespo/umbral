//go:build unix

package remote

import (
	"errors"
	"syscall"
)

// alive reports whether a process of this pid exists; one this user may not signal is taken
// for alive.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
