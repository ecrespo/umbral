package agents

import (
	"runtime"
	"testing"
)

// posixLines skips a test whose outcome is the POSIX reading of a command line — a line that
// runs unasked, is remembered or asks for a reason the parser gives. On Windows every line asks
// as windows_shell (delta `2026-10-windows-port`, decision 14), which
// TestFullAutoAsksForEveryCommandOnWindows_REQ_WIN_001 checks through the runtime.
func posixLines(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("decided by the POSIX reading of the line; on Windows every line asks (decision 14)")
	}
}
