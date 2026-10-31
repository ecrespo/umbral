//go:build unix

package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProgram writes a program a session can run, named name in dir, and returns the path a
// lookup resolves it to: on Unix a script with the executable bit.
func writeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
