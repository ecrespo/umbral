package sshtunnel

import "syscall"

// sysProcAttr ends ssh with this daemon: a kill -9 of umbrald leaves no forward behind
// (REQ-RMT-002). The signal follows the thread that started ssh; Go's runtime keeps its threads
// for the process's life unless a goroutine locked to one exits, which this package never does.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
