//go:build !windows

package domain

// shellLinesUnread is false: the destructive floor reads a POSIX shell's lines.
const shellLinesUnread = false

// foldPath is a path as written: Unix file systems compare paths byte for byte.
func foldPath(p string) string { return p }
