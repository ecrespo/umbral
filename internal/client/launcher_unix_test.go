//go:build unix

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeLauncher writes a one-line shell script that runs self with the fake daemon's marker
// set to mode, for the child only.
func writeLauncher(t *testing.T, dir, name, mode, self string) string {
	t.Helper()
	script := filepath.Join(dir, name)
	body := fmt.Sprintf("#!/bin/sh\nexec env %s=%s %q \"$@\"\n", fakeDaemonEnv, mode, self)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write the %s launcher: %v", name, err)
	}
	return script
}
