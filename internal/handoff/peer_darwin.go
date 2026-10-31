package handoff

import (
	"net"

	"golang.org/x/sys/unix"
)

// peer is the uid and pid of the process at the other end, from LOCAL_PEERCRED and
// LOCAL_PEERPID: the kernel's record, not anything the peer says.
func peer(c *net.UnixConn) (uid, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, 0, err
	}
	uid, cerr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		if cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); cerr != nil {
			return
		}
		if pid, cerr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); cerr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return -1, 0, err
	}
	return uid, pid, cerr
}
