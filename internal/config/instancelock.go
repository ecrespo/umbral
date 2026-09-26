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

// ErrDatabaseInUse reports that another daemon holds the database's lock.
var ErrDatabaseInUse = errors.New("config: another umbrald is using this database")

// DatabaseLockSuffix names the database's lock file: `umbral.db.lock` beside `umbral.db`.
const DatabaseLockSuffix = ".lock"

// AcquireDatabaseLock takes the exclusive, non-blocking lock that makes a daemon the only one
// using a database, and returns ErrDatabaseInUse when someone else holds it (delta
// `2026-09-database-lock`).
//
// The instance lock guards the runtime directory, and recovery's premise — every `alive`
// row belongs to a process that is gone — is about the database. The two coincide only while
// both paths are defaulted: a daemon started with another `--socket` and the same `--db`
// holds an instance lock of its own and would recover over the first daemon's live sessions.
// A separate file rather than the database itself, because SQLite takes POSIX locks on that
// file and mixing lock families on one file behaves differently across platforms.
func AcquireDatabaseLock(dbPath string) (*InstanceLock, error) {
	dir := filepath.Dir(dbPath)
	//nolint:gosec // dbPath is the operator's -db flag or the XDG data directory, never a peer's input
	if err := os.MkdirAll(dir, RuntimeDirMode); err != nil {
		return nil, fmt.Errorf("config: create the database directory: %w", err)
	}
	return acquireLock(dbPath+DatabaseLockSuffix, ErrDatabaseInUse)
}

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
	return acquireLock(filepath.Join(dir, LockFileName), ErrAlreadyRunning)
}

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
	//nolint:gosec // name is the lock this process created and holds, from the operator's -socket or -db flag or the XDG directories
	if rmErr := os.Remove(name); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
		err = rmErr
	}
	return err
}
