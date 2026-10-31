//go:build unix

package config

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes an exclusive, non-blocking flock on path, answering busy when another
// open file description holds it.
func acquireLock(path string, busy error) (*InstanceLock, error) {
	//nolint:gosec // path is derived from the operator's -socket or -db flag, or from the XDG directories, never from a peer
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: open the lock %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", busy, path)
		}
		return nil, fmt.Errorf("config: lock %s: %w", path, err)
	}
	return &InstanceLock{f: f}, nil
}
