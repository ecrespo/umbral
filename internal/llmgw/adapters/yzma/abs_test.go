package yzma

import (
	"path/filepath"
	"runtime"
)

// abs makes a test's POSIX path absolute on this platform: as it is on Unix, on drive C: on
// Windows, where a rooted path without a drive is not absolute (delta `2026-10-windows-port`).
func abs(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}
	return p
}

// The library directories the tests configure; none is read, since the engine is a fake.
var (
	optLlama = abs("/opt/llama")
	optOther = abs("/opt/other")
)
