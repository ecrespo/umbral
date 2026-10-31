package domain

import "strings"

// shellLinesUnread is true: an agent's shell on Windows is PowerShell or cmd, whose lines the
// POSIX parser does not read, so every line asks (delta `2026-10-windows-port`, decision 14).
const shellLinesUnread = true

// foldPath is a path as NTFS compares it: without case, `/` read as `\` (decision 15).
func foldPath(p string) string { return strings.ToLower(strings.ReplaceAll(p, "/", `\`)) }
