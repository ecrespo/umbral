package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProgram writes a program a session can run, named name in dir, and returns the path a
// lookup resolves it to: on Windows a batch file, found through PATHEXT as name.cmd.
func writeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+".cmd")
	if err := os.WriteFile(path, []byte("@exit /b 0\r\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
