//go:build unix

package skills

import (
	"syscall"
	"testing"
)

// mkfifo makes a FIFO at path; it reports whether the platform has them.
func mkfifo(t *testing.T, path string) bool {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return true
}
