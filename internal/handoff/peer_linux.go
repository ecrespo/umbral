package handoff

import (
	"net"

	"golang.org/x/sys/unix"
)

// peer is the uid and pid of the process at the other end, from SO_PEERCRED: the kernel's
// record, not anything the peer says.
func peer(c *net.UnixConn) (uid, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, 0, err
	}
	uid, cerr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		if cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); cerr == nil {
			uid, pid = int(cred.Uid), int(cred.Pid)
		}
	}); err != nil {
		return -1, 0, err
	}
	return uid, pid, cerr
}
