//go:build unix

package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeProgram writes a program named name in dir that prints stdout and stderr, each a line or
// nothing, and exits with code; it returns the program's path. On Unix it is a shell script.
func fakeProgram(t *testing.T, dir, name, stdout, stderr string, code int) string {
	t.Helper()
	body := "#!/bin/sh\n"
	if stdout != "" {
		body += "echo '" + stdout + "'\n"
	}
	if stderr != "" {
		body += "echo '" + stderr + "' >&2\n"
	}
	body += "exit " + strconv.Itoa(code) + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
