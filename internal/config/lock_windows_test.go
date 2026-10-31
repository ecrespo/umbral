package config

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestOnlyOneLockIsGrantedOnWindows_REQ_WIN_001: decision 8's LockFileEx keeps the instance and
// database locks' invariant — one holder, and the next daemon takes it once released. A lock is
// per handle, so a second handle in this process contends as a second daemon would.
func TestOnlyOneLockIsGrantedOnWindows_REQ_WIN_001(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("the first instance lock was refused: %v", err)
	}
	if second, err := AcquireInstanceLock(dir); !errors.Is(err, ErrAlreadyRunning) {
		if second != nil {
			_ = second.Release()
		}
		t.Fatalf("a second instance lock: %v, want ErrAlreadyRunning", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("the instance lock was not released for the next daemon: %v", err)
	}
	_ = again.Release()

	db := filepath.Join(t.TempDir(), "umbral.db")
	held, err := AcquireDatabaseLock(db)
	if err != nil {
		t.Fatalf("the first database lock was refused: %v", err)
	}
	defer func() { _ = held.Release() }()
	if second, err := AcquireDatabaseLock(db); !errors.Is(err, ErrDatabaseInUse) {
		if second != nil {
			_ = second.Release()
		}
		t.Fatalf("a second database lock: %v, want ErrDatabaseInUse", err)
	}
}
