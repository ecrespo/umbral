package client

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachAttr starts the daemon in a process group of its own and without the console, so it
// survives the window that started it and never receives the Ctrl-C meant for a foreground
// `umb`, as Setsid does on Unix.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
