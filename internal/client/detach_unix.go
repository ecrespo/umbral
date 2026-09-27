//go:build unix

package client

import "syscall"

// detachAttr puts the daemon in its own session, so it survives the terminal that started
// it and never receives the SIGINT meant for a foreground `umb`. Without Setsid a Ctrl-C
// aimed at the CLI would take the daemon down with it, and with it every session it owns.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
