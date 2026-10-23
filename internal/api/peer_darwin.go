package api

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID is the pid of the process at the other end of a Unix socket, from LOCAL_PEERPID: the
// kernel's record of who connected, not anything the client says.
func peerPID(nc net.Conn) int {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if p, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			pid = p
		}
	})
	return pid
}
