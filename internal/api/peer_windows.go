package api

import (
	"net"

	"golang.org/x/sys/windows"
)

// peerPID is the pid of the process at the other end of the daemon's pipe, from
// GetNamedPipeClientProcessId: the kernel's record of who connected, as SO_PEERCRED is on Linux
// (delta `2026-10-windows-port`, decision 4). Anything else reads 0, which the agent check
// refuses.
func peerPID(nc net.Conn) int {
	h, ok := nc.(interface{ Fd() uintptr })
	if !ok {
		return 0
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(h.Fd()), &pid); err != nil {
		return 0
	}
	return int(pid)
}
