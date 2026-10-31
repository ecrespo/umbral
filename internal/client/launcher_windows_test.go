package client

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeLauncher writes a batch file that runs self with the fake daemon's marker set to mode,
// for the child only: Windows starts a .cmd through cmd.exe as it would a program.
func writeLauncher(t *testing.T, dir, name, mode, self string) string {
	t.Helper()
	script := filepath.Join(dir, name+".cmd")
	body := fmt.Sprintf("@echo off\r\nset %s=%s\r\n\"%s\" %%*\r\n", fakeDaemonEnv, mode, self)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write the %s launcher: %v", name, err)
	}
	return script
}
