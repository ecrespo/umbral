package builtin

import (
	"path/filepath"
	"runtime"
)

// abs makes a test's POSIX path absolute on this platform: as it is on Unix, on drive C: on
// Windows, where /work is rooted but not absolute (delta `2026-10-windows-port`).
func abs(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}
	return p
}

// work is the working directory and write root the terminal tests lend a call.
var work = abs("/work")
