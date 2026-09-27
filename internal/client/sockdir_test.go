package client

import (
	"os"
	"testing"
)

// socketDir is a short temporary directory for a Unix socket, removed when the test ends.
//
// A socket path is capped at 104 bytes on macOS and 108 on Linux, and t.TempDir under macOS's
// $TMPDIR — `/var/folders/…/T/` — plus a test's name reaches that before the socket's own name
// is added, so every listener failed with "bind: invalid argument" on the macOS runner. /tmp
// keeps it short on both. The Test prefix keeps a leaked directory visible to the hygiene gate,
// which looks for `Test*` in the temporary directory.
func socketDir(tb testing.TB) string {
	tb.Helper()
	base := "/tmp"
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "TestSock")
	if err != nil {
		tb.Fatalf("create a socket directory: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
