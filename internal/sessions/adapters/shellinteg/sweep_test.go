package shellinteg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootstrapDirectoriesLiveInTheRuntimeDirectory_REQ_TERM_012 pins where a bootstrap's
// files are written.
//
// They used to land in `os.MkdirTemp("", …)` — `/tmp` on a normal Linux system, shared with
// every other program and every other Umbral installation on the machine. That is what makes
// a sweeper unsafe to write: a daemon cleaning `/tmp/umbral-shellinteg-*` can delete the
// directory of a session another installation's daemon is starting at that moment. The
// requirement therefore moves the files before it asks anyone to remove them.
func TestBootstrapDirectoriesLiveInTheRuntimeDirectory_REQ_TERM_012(t *testing.T) {
	t.Parallel()

	runtime := t.TempDir()
	b, err := Prepare("/bin/bash", runtime)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = b.Close() }()

	if got := b.dir; !strings.HasPrefix(got, runtime+string(os.PathSeparator)) {
		t.Errorf("bootstrap directory = %q, want one under the runtime directory %q", got, runtime)
	}
	if base := filepath.Base(b.dir); !strings.HasPrefix(base, dirPrefix) {
		t.Errorf("bootstrap directory %q does not carry the %q prefix the sweeper matches on", base, dirPrefix)
	}
}

// TestPrepareRefusesToGuessTheBootstrapDirectory_REQ_TERM_012 keeps the wiring mistake loud.
//
// An empty parent is exactly what `os.MkdirTemp` turns back into `/tmp`, so accepting one
// would restore the old behaviour silently on the day someone constructs the adapter without
// its directory.
func TestPrepareRefusesToGuessTheBootstrapDirectory_REQ_TERM_012(t *testing.T) {
	t.Parallel()

	if _, err := Prepare("/bin/bash", ""); err == nil {
		t.Fatal("Prepare with no parent directory succeeded; want an error rather than a fallback to /tmp")
	}
}

// TestSweepRemovesOrphansAndNothingElse_REQ_TERM_012 is the unit half of the sweep: what it
// takes, and — the half with teeth — what it must leave.
//
// `os.RemoveAll(runtimeDir)` would pass any test that only checks the orphan is gone, and
// would delete the socket, the token and the instance lock the daemon is holding.
func TestSweepRemovesOrphansAndNothingElse_REQ_TERM_012(t *testing.T) {
	t.Parallel()

	runtime := t.TempDir()
	orphans := []string{dirPrefix + "1111", dirPrefix + "2222"}
	for _, name := range orphans {
		dir := filepath.Join(runtime, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "umbral.bash"), []byte("# rc"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Everything else the daemon keeps in that directory.
	keep := []string{"umbral.sock", "umbral.lock", "token", "umbrald.log"}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(runtime, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := Sweep(runtime)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != len(orphans) {
		t.Errorf("Sweep removed %d directories, want %d", removed, len(orphans))
	}
	for _, name := range orphans {
		if _, err := os.Stat(filepath.Join(runtime, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep: %v", name, err)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(runtime, name)); err != nil {
			t.Errorf("the sweep removed %s, which belongs to the running daemon: %v", name, err)
		}
	}
}

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

// TestSweepOnAMissingDirectoryIsNotAnError_REQ_TERM_012 covers the first start on a machine,
// where the runtime directory holds nothing at all.
func TestSweepOnAMissingDirectoryIsNotAnError_REQ_TERM_012(t *testing.T) {
	t.Parallel()

	removed, err := Sweep(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Errorf("Sweep on a missing directory: %v", err)
	}
	if removed != 0 {
		t.Errorf("Sweep removed %d directories from a directory that does not exist", removed)
	}
}
