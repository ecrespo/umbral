package config

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// acquireLock takes an exclusive, non-blocking LockFileEx on path, answering busy when another
// handle holds it. As flock, the lock dies with the handle, however its process dies (delta
// `2026-10-windows-port`, decision 8).
func acquireLock(path string, busy error) (*InstanceLock, error) {
	//nolint:gosec // path is derived from the operator's -socket or -db flag, or from the user's directories, never from a peer
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: open the lock %s: %w", path, err)
	}
	var whole windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &whole)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w: %s", busy, path)
		}
		return nil, fmt.Errorf("config: lock %s: %w", path, err)
	}
	return &InstanceLock{f: f}, nil
}
