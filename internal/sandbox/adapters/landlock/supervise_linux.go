package landlock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// A process that may bind a port must not be reachable from outside the machine (REQ-SBX-008,
// delta `2026-11-model-relay`, piece a2). Landlock's rule is by port alone, so a bind on every
// interface passes it, and it has no right for `listen`, so a socket that listens without a
// bind gets a port of the kernel's choosing. seccomp cannot read the address either. What it
// can do is hand the call to another process: the filter answers `listen` with a user
// notification, and the helper stays as the command's parent to receive them.
//
// For each `listen` the supervisor takes its own duplicate of the caller's socket, reads the
// address it is bound to, and — for a loopback address on a port the profile lets it bind —
// performs the listen itself on that duplicate, which is the same socket. It never tells the
// kernel to go on with the caller's call: between the look and the call the caller could
// have put another socket under the same number. A socket it has not approved is never
// listened on, whatever the caller does.
//
// The helper is then two stages of the same binary. The first starts the second with one end
// of a socket pair, receives the notification descriptor over it, and supervises. The second
// confines itself, sends the descriptor up, and becomes the command.

// stageEnv tells the helper it is the second stage, and names the descriptor it sends its
// notification descriptor up. It is taken out of the command's environment.
const stageEnv = "UMBRAL_SANDBOX_NOTIFY_FD"

// stageFD is that descriptor: the first of exec's extra files.
const stageFD = 3

// supervised reports whether p needs a supervisor: it lets the process bind a port, which
// is the only reason it has to listen.
func supervised(p domain.Profile) bool { return p.Net != nil && len(p.Net.Bind) > 0 }

// supervise runs the helper again as the command's confined parent-to-be and answers its
// `listen` calls until it ends. It does not return on success: the helper exits as the
// command did.
func supervise(p domain.Profile, arg string, argv []string) error {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("sandbox: supervisor: %w", err)
	}
	up, down := os.NewFile(uintptr(pair[0]), "notify-up"), os.NewFile(uintptr(pair[1]), "notify-down")
	// The command dies with this thread, which is the one that starts it and then waits for
	// it: a command left behind would find nobody to answer its listen, and would be a
	// process the daemon believes it stopped.
	runtime.LockOSThread()
	cmd := exec.CommandContext(context.Background(), "/proc/self/exe", append([]string{HelperFlag, arg, "--"}, argv...)...) // #nosec G204 -- this binary, as the helper it already is
	cmd.Args[0] = os.Args[0]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{down}
	cmd.Env = append(os.Environ(), stageEnv+"="+strconv.Itoa(stageFD))
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGUSR2)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sandbox: supervisor: %w", err)
	}
	_ = down.Close()
	go func() {
		for s := range signals {
			_ = cmd.Process.Signal(s)
		}
	}()
	// A second stage that fails before it is confined sends nothing: the read ends when it
	// exits, and its exit code is the helper's.
	if notify, err := receiveFD(up); err == nil {
		go serveListens(notify, p.Net.Bind)
	}
	_ = up.Close()
	err = cmd.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &exit):
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			os.Exit(128 + int(status.Signal()))
		}
		os.Exit(exit.ExitCode())
	}
	return fmt.Errorf("sandbox: supervisor: %w", err)
}

// handUp sends the notification descriptor to the first stage over the descriptor stageEnv
// names, and closes both: the command starts with neither.
func handUp(notify int) error {
	fd, err := strconv.Atoi(os.Getenv(stageEnv))
	if err != nil || fd != stageFD {
		return errors.New("sandbox: supervisor: no descriptor to send the listener up")
	}
	defer func() { _ = unix.Close(fd); _ = unix.Close(notify) }()
	if err := unix.Sendmsg(fd, []byte{0}, unix.UnixRights(notify), nil, 0); err != nil {
		return fmt.Errorf("sandbox: supervisor: send the listener up: %w", err)
	}
	return nil
}

// receiveFD reads the one descriptor the second stage sends.
func receiveFD(up *os.File) (int, error) {
	buf, oob := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	for {
		_, oobn, _, _, err := unix.Recvmsg(int(up.Fd()), buf, oob, unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return -1, err
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil || len(msgs) != 1 {
			return -1, errors.New("sandbox: supervisor: no listener received")
		}
		fds, err := unix.ParseUnixRights(&msgs[0])
		if err != nil || len(fds) != 1 {
			return -1, errors.New("sandbox: supervisor: no listener received")
		}
		return fds[0], nil
	}
}

// seccompNotif is struct seccomp_notif, 80 bytes, and seccompNotifResp struct
// seccomp_notif_resp, 24: the sizes the two ioctl numbers carry.
type seccompNotif struct {
	id    uint64
	pid   uint32
	flags uint32
	data  struct {
		nr   int32
		arch uint32
		ip   uint64
		args [6]uint64
	}
}

type seccompNotifResp struct {
	id    uint64
	val   int64
	error int32
	flags uint32
}

// serveListens answers every `listen` of the confined processes until the last of them is
// gone. A call it cannot decide is refused.
func serveListens(notify int, ports []uint16) {
	// Closed when this stops for any reason, so a `listen` that follows fails instead of
	// waiting for an answer nobody gives.
	defer func() { _ = unix.Close(notify) }()
	for {
		var req seccompNotif
		if err := ioctlPtr(notify, unix.SECCOMP_IOCTL_NOTIF_RECV, unsafe.Pointer(&req)); err != nil { // #nosec G103 -- the kernel fills the struct during the call
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ENOENT) {
				// ENOENT: the caller was killed before the notification was read.
				continue
			}
			return
		}
		resp := seccompNotifResp{id: req.id}
		if errno := listenFor(notify, req, ports); errno != 0 {
			resp.error = -int32(errno)
		}
		// A caller that is gone by now has no answer to receive.
		_ = ioctlPtr(notify, unix.SECCOMP_IOCTL_NOTIF_SEND, unsafe.Pointer(&resp)) // #nosec G103 -- the kernel reads the struct during the call
	}
}

// listenFor performs the `listen` req asks for when its socket is bound to a loopback address
// on one of ports, and says what the caller's call returns: 0, or the error.
func listenFor(notify int, req seccompNotif, ports []uint16) unix.Errno {
	if req.data.nr != unix.SYS_LISTEN {
		return unix.ENOSYS
	}
	// The notification names a thread. Its group's leader is what a pidfd opens on every
	// kernel this runs on; should the thread not share its group's descriptors, the socket
	// read below is another one — and is still only listened on if it passes.
	group := leader(int(req.pid))
	if !descends(group, os.Getpid()) {
		// A process that left the supervisor's tree — reparented after its parent exited —
		// is refused. Where the kernel's ptrace policy is the usual one it could not be
		// served anyway, since only a descendant's descriptors can be taken; this makes it
		// the same everywhere.
		return unix.EACCES
	}
	pidfd, err := unix.PidfdOpen(group, 0)
	if err != nil {
		return unix.EACCES
	}
	defer func() { _ = unix.Close(pidfd) }()
	sock, err := unix.PidfdGetfd(pidfd, int(int32(req.data.args[0])), 0) // #nosec G115 -- a descriptor number, as the kernel reads the argument
	if err != nil {
		if errors.Is(err, unix.EBADF) {
			return unix.EBADF
		}
		return unix.EACCES
	}
	defer func() { _ = unix.Close(sock) }()
	// The pid may have been another process's by the time it was opened: the notification
	// is still the caller's only if the kernel says so now.
	if err := ioctlPtr(notify, unix.SECCOMP_IOCTL_NOTIF_ID_VALID, unsafe.Pointer(&req.id)); err != nil { // #nosec G103 -- the kernel reads the id during the call
		return unix.EACCES
	}
	addr, err := unix.Getsockname(sock)
	if err != nil {
		if errno, ok := errors.AsType[unix.Errno](err); ok {
			return errno
		}
		return unix.EACCES
	}
	if !loopbackPort(addr, ports) {
		return unix.EACCES
	}
	if err := unix.Listen(sock, int(int32(req.data.args[1]))); err != nil { // #nosec G115 -- the backlog, as the kernel reads the argument
		if errno, ok := errors.AsType[unix.Errno](err); ok {
			return errno
		}
		return unix.EACCES
	}
	return 0
}

// loopbackPort reports whether addr is a loopback address of either Internet family on one
// of ports. An address mapped from the other family, and a socket of any other kind, is not.
func loopbackPort(addr unix.Sockaddr, ports []uint16) bool {
	switch a := addr.(type) {
	case *unix.SockaddrInet4:
		return a.Addr[0] == 127 && a.Port > 0 && a.Port <= 0xffff && slices.Contains(ports, uint16(a.Port))
	case *unix.SockaddrInet6:
		return a.Addr == [16]byte{15: 1} && a.Port > 0 && a.Port <= 0xffff && slices.Contains(ports, uint16(a.Port))
	}
	return false
}

// descends reports whether pid has ancestor among its parents, read from /proc. A process
// that cannot be read does not.
func descends(pid, ancestor int) bool {
	for range 64 {
		if pid == ancestor {
			return true
		}
		if pid <= 1 {
			return false
		}
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		// The parent is the second field after the command's name, which is in
		// parentheses and may hold any byte: read from the last one.
		i := bytes.LastIndexByte(stat, ')')
		fields := bytes.Fields(stat[i+1:])
		if i < 0 || len(fields) < 2 {
			return false
		}
		parent, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return false
		}
		pid = parent
	}
	return false
}

// leader is the thread group a thread belongs to, or the thread itself when it cannot be
// read.
func leader(tid int) int {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(tid) + "/status")
	if err != nil {
		return tid
	}
	for line := range bytes.Lines(status) {
		if rest, ok := bytes.CutPrefix(line, []byte("Tgid:")); ok {
			if tgid, err := strconv.Atoi(string(bytes.TrimSpace(rest))); err == nil {
				return tgid
			}
		}
	}
	return tid
}

func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}
