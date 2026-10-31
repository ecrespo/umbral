package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeProgram writes a program named name in dir that prints stdout and stderr, each a line or
// nothing, and exits with code; it returns the program's path. On Windows it is a batch file,
// name.cmd, which a process runs as it runs an executable.
func fakeProgram(t *testing.T, dir, name, stdout, stderr string, code int) string {
	t.Helper()
	body := "@echo off\r\n"
	if stdout != "" {
		body += "echo " + stdout + "\r\n"
	}
	if stderr != "" {
		body += "echo " + stderr + " 1>&2\r\n"
	}
	body += "exit /b " + strconv.Itoa(code) + "\r\n"
	path := filepath.Join(dir, name+".cmd")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
