package pty

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// exitWatch is a kqueue with an EVFILT_PROC NOTE_EXIT filter on an adopted child: it fires
// when the child exits, whoever its parent is.
type exitWatch struct {
	kq int
	// gone is a child that had already exited when the watch was taken.
	gone bool
}

func watchExit(pid int) (exitWatch, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return exitWatch{}, fmt.Errorf("pty: watch pid %d: %w", pid, err)
	}
	change := unix.Kevent_t{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC, //nolint:gosec // a pid is positive
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kq)
		if errors.Is(err, unix.ESRCH) {
			return exitWatch{kq: -1, gone: true}, nil
		}
		return exitWatch{}, fmt.Errorf("pty: watch pid %d: %w", pid, err)
	}
	return exitWatch{kq: kq}, nil
}

// wait blocks until the child has exited and reports ports.ErrExitUnknown, the only answer a
// daemon that is not its parent has.
func (w exitWatch) wait() error {
	if w.gone {
		return ports.ErrExitUnknown
	}
	defer w.close()
	events := make([]unix.Kevent_t, 1)
	for {
		_, err := unix.Kevent(w.kq, nil, events, nil)
		if err == nil {
			return ports.ErrExitUnknown
		}
		if !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("pty: wait for the adopted child: %w", err)
		}
	}
}

func (w exitWatch) close() {
	if w.kq > 0 {
		_ = unix.Close(w.kq)
	}
}
