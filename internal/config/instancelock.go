//go:build unix

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockFileName is the file whose advisory lock marks "one daemon owns this installation".
// It sits in the runtime directory beside the socket and the token, so the three share a
// lifetime: the directory is cleared at logout and the lock goes with it.
const LockFileName = "umbrald.lock"

// ErrAlreadyRunning reports that another daemon holds the installation.
var ErrAlreadyRunning = errors.New("config: another umbrald already owns this installation")

// InstanceLock is a held lock. Release closes it, which drops the lock, and removes the
// file.
type InstanceLock struct {
	f *os.File
}

// AcquireInstanceLock takes the exclusive, non-blocking lock that makes a daemon the only
// one for a runtime directory. It returns ErrAlreadyRunning when someone else holds it.
//
// Every daemon has to take this **before** it opens the database, because startup
// recovery (Data Model §6) rewrites live rows: it marks every `alive` session `exited` and
// every open block `abandoned`, on the premise that the PTYs died with the previous
// process. A second daemon running that over a first daemon's running sessions destroys
// state that is not stale at all, and then unlinks the first one's socket on its way to
// binding its own. Autostart makes concurrent starts ordinary rather than exotic — a
// prompt hook calling `umb` in several panes at once is enough — so the race is a matter
// of when, not whether.
//
// The lock is `flock`, not a pid file: the kernel drops it when the holder dies, however
// it dies, so a killed daemon leaves nothing to clean up and no stale pid to misread.
func AcquireInstanceLock(dir string) (*InstanceLock, error) {
	//nolint:gosec // dir is the runtime directory: RuntimeDir() or the operator's -socket flag, never a peer's input
	if err := os.MkdirAll(dir, RuntimeDirMode); err != nil {
		return nil, fmt.Errorf("config: create the runtime directory: %w", err)
	}
	path := filepath.Join(dir, LockFileName)

	//nolint:gosec // dir comes from the operator's -socket flag or from RuntimeDir, never from a peer
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: open the instance lock %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("config: lock %s: %w", path, err)
	}
	return &InstanceLock{f: f}, nil
}

// Release drops the lock. The file is removed as a courtesy; the lock itself is gone the
// moment the descriptor closes, so a daemon that is killed instead of stopped leaves the
// file behind and the next daemon takes it over without noticing.
func (l *InstanceLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	name := l.f.Name()
	err := l.f.Close()
	l.f = nil
	if rmErr := os.Remove(name); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
		err = rmErr
	}
	return err
}
