//go:build unix

package shellinteg

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSweepReportsWhatItCouldNotRemove_REQ_TERM_012 is the caller's half of "log and
// continue": the daemon can only log what the sweep tells it about.
func TestSweepReportsWhatItCouldNotRemove_REQ_TERM_012(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so no removal can be made to fail here")
	}

	runtime := t.TempDir()
	stuck := filepath.Join(runtime, dirPrefix+"stuck")
	if err := os.MkdirAll(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "umbral.bash"), []byte("# rc"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without write permission on the directory the child cannot be unlinked, so RemoveAll
	// fails the way a permission the user changed would make it fail.
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })

	gone := filepath.Join(runtime, dirPrefix+"gone")
	if err := os.MkdirAll(gone, 0o700); err != nil {
		t.Fatal(err)
	}

	removed, err := Sweep(runtime)
	if err == nil {
		t.Fatal("Sweep reported no error although a directory could not be removed")
	}
	if removed != 1 {
		t.Errorf("Sweep removed %d directories, want 1: the failure must not stop the others", removed)
	}
	if _, statErr := os.Stat(gone); !os.IsNotExist(statErr) {
		t.Errorf("the removable orphan survived a failure on an unrelated one: %v", statErr)
	}
}
