//go:build unix

package acptest

import "syscall"

// ownGroup moves a child to a process group of its own.
func ownGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
