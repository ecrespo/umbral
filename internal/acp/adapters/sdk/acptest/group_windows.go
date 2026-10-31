package acptest

import "syscall"

// ownGroup has no POSIX process group to move to on Windows.
func ownGroup() *syscall.SysProcAttr { return nil }
