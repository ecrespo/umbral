package pty

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// exitWatch is a pidfd on an adopted child: it becomes readable when the child exits, whoever
// its parent is.
type exitWatch struct {
	fd int
}

func watchExit(pid int) (exitWatch, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return exitWatch{}, fmt.Errorf("pty: watch pid %d: %w", pid, err)
	}
	return exitWatch{fd: fd}, nil
}

// wait blocks until the child has exited and reports ports.ErrExitUnknown, the only answer a
// daemon that is not its parent has.
func (w exitWatch) wait() error {
	defer w.close()
	fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, -1)
		if err == nil {
			return ports.ErrExitUnknown
		}
		if !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("pty: wait for the adopted child: %w", err)
		}
	}
}

func (w exitWatch) close() {
	if w.fd > 0 {
		_ = unix.Close(w.fd)
	}
}
