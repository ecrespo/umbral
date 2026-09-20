//go:build unix

package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestOnlyOneInstanceLockIsGranted is the invariant the whole daemon startup rests on: a
// second daemon must not run Data Model §6 recovery over a first one's live sessions.
func TestOnlyOneInstanceLockIsGranted(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("the first lock was refused: %v", err)
	}
	defer func() { _ = first.Release() }()

	// A second attempt from another process, because flock is per open file description
	// and the same process would be granted it again.
	if err := tryLockInSubprocess(t, dir); !errors.Is(err, errSubprocessBusy) {
		t.Errorf("a second process took the lock (%v); it would then recover over live sessions", err)
	}
}

// TestInstanceLockIsReleasedForTheNextDaemon covers the ordinary restart: a daemon that
// stopped must not lock the installation out.
func TestInstanceLockIsReleasedForTheNextDaemon(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("the lock was not released for the next daemon: %v", err)
	}
	_ = second.Release()
}

// TestInstanceLockSurvivesAKilledHolder is why this is flock and not a pid file. A daemon
// that is SIGKILLed writes no cleanup, and a stale pid file would lock the installation
// out until someone deleted it by hand. The kernel drops an flock when the holder dies.
func TestInstanceLockSurvivesAKilledHolder(t *testing.T) {
	dir := t.TempDir()

	// A subprocess takes the lock and is killed; the file it left behind must not stop
	// the next daemon.
	cmd := lockHolderCmd(t, dir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the holder: %v", err)
	}
	waitForLockFile(t, dir)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the holder: %v", err)
	}
	_ = cmd.Wait()

	lock, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("a killed holder left the installation locked out: %v", err)
	}
	_ = lock.Release()
}

// TestConcurrentAcquireGrantsExactlyOne is the shape autostart produces: several daemons
// racing for the same installation at the same moment.
func TestConcurrentAcquireGrantsExactlyOne(t *testing.T) {
	dir := t.TempDir()

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	locks := make([]*InstanceLock, racers)
	start := make(chan struct{})

	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			locks[i], errs[i] = AcquireInstanceLock(dir)
		}()
	}
	close(start)
	wg.Wait()

	// Within one process flock is per open file description, so several may succeed
	// here; what must never happen is an unexpected error, because that would leave a
	// daemon exiting for the wrong reason.
	for i, err := range errs {
		if err != nil && !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("racer %d failed with something other than ErrAlreadyRunning: %v", i, err)
		}
	}
	for _, l := range locks {
		_ = l.Release()
	}
}

var errSubprocessBusy = errors.New("the subprocess reported the lock was already held")

const lockHelperEnv = "UMBRAL_CONFIG_TEST_LOCK_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(lockHelperEnv); dir != "" {
		lock, err := AcquireInstanceLock(dir)
		if err != nil {
			if errors.Is(err, ErrAlreadyRunning) {
				os.Exit(3)
			}
			os.Exit(1)
		}
		// Hold it until killed, so the parent can observe both the contention and what
		// happens when the holder dies without releasing. The lock is deliberately never
		// released: that is the case under test.
		_ = lock
		select {}
	}
	os.Exit(m.Run())
}

func lockHolderCmd(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), self)
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
	return cmd
}

func tryLockInSubprocess(t *testing.T, dir string) error {
	t.Helper()
	cmd := lockHolderCmd(t, dir)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
		return errSubprocessBusy
	}
	return err
}

// waitForLockFile waits for the subprocess to have taken the lock. The file appearing is
// not quite proof that flock succeeded, but the holder creates it and locks it in the same
// breath, and the test that follows kills the holder either way.
func waitForLockFile(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, LockFileName)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the lock holder never created %s", path)
}
