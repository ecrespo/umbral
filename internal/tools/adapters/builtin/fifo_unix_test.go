//go:build unix

package builtin

import (
	"syscall"
	"testing"
)

// mkfifo makes a FIFO at path.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
