//go:build linux

package landlock

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// The file-system rights, by the ABI that introduced them.
const (
	rightsV1 = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	// readRights are what every readable path gets: reading, listing and running.
	readRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	// fileRights are the rights a rule on a file, not a directory, may carry.
	fileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
)

// Landlock's network rules. golang.org/x/sys names the two TCP rights and not yet the rule's
// type or its attribute (include/uapi/linux/landlock.h).
const (
	accessNetBindTCP    = unix.LANDLOCK_ACCESS_NET_BIND_TCP
	accessNetConnectTCP = unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	ruleNetPort         = 2
	// netABI is the first Landlock with network rules (kernel 6.7).
	netABI = 4
)

// netPortAttr is struct landlock_net_port_attr.
type netPortAttr struct {
	allowedAccess uint64
	port          uint64
}

// minABI is the oldest Landlock this adapter enforces with. Before version 3 the kernel does
// not mediate truncate, so a confined process could empty any file its user can write — the
// database included — and "confined" would not mean what REQ-SBX-001 says.
const minABI = 3

// abi asks the kernel for its Landlock version; zero is none.
func abi() int {
	v, _, errno := syscall.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || int(v) < 1 {
		return 0
	}
	return int(v)
}

func probe() domain.Status {
	switch version := abi(); {
	case version == 0:
		return domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonNoLandlock}
	case version < minABI:
		return domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonOldLandlock}
	case auditArch == 0:
		return domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonUnsupportedArch}
	}
	return domain.Status{State: domain.StateEnforcing, Mechanism: Mechanism, Network: abi() >= netABI}
}

// handled are the rights the ruleset governs on this kernel: every one it knows, so nothing
// it can refuse is left open.
func handled(version int) uint64 {
	rights := uint64(rightsV1)
	if version >= 2 {
		rights |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if version >= 3 {
		rights |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if version >= 5 {
		rights |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return rights
}

// Exec is the helper's body: it confines this process to the profile in arg and replaces it
// with argv. It returns only on failure, and then nothing of argv has run — a profile that
// cannot be applied is never a reason to run the command without one.
func Exec(arg string, argv []string) error {
	p, err := domain.Decode(arg)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("sandbox: no command to confine")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if supervised(p) && os.Getenv(stageEnv) == "" {
		// A process that may bind is not left alone with `listen`: this process stays as
		// its supervisor, and the command is run by a second stage.
		return supervise(p, arg, argv)
	}
	// Landlock and no_new_privs are per thread, and exec keeps the calling thread's.
	runtime.LockOSThread()
	notify, err := restrict(p)
	if err != nil {
		return err
	}
	if notify >= 0 {
		if err := handUp(notify); err != nil {
			return err
		}
	}
	if err := os.Unsetenv(stageEnv); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	return fmt.Errorf("sandbox: exec %s: %w", path, syscall.Exec(path, argv, os.Environ())) // #nosec G204 -- the command the daemon was configured to run, now confined
}

// restrict confines the calling thread to p. Where p lets the process bind a port it returns
// the descriptor its `listen` calls are notified on, and -1 otherwise.
func restrict(p domain.Profile) (int, error) {
	version := abi()
	if version < minABI || auditArch == 0 {
		return -1, errors.New("sandbox: this system cannot confine a process")
	}
	if p.Net != nil && version < netABI {
		return -1, errors.New("sandbox: this kernel's Landlock cannot confine the network")
	}
	rights := handled(version)
	attr := unix.LandlockRulesetAttr{Access_fs: rights}
	if p.Net != nil {
		// Every TCP bind and connect is governed, and only the ports below are allowed.
		attr.Access_net = accessNetBindTCP | accessNetConnectTCP
	}
	if version >= 6 {
		// No signal to a process outside the sandbox — the daemon, the user's shells — and no
		// connection to an abstract Unix socket made outside it.
		attr.Scoped = unix.LANDLOCK_SCOPE_SIGNAL | unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
	}
	fd, _, errno := syscall.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0) // #nosec G103 -- the kernel reads the struct during the call
	if errno != 0 {
		return -1, fmt.Errorf("sandbox: create the ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer func() { _ = unix.Close(ruleset) }()

	denied := make([]string, 0, len(p.DenyRead))
	for _, d := range p.DenyRead {
		denied = append(denied, resolve(d))
	}
	for _, path := range p.Write {
		if err := allow(ruleset, path, rights, rights, nil); err != nil {
			return -1, err
		}
	}
	for _, path := range domain.Devices {
		if err := allow(ruleset, path, rights&^unix.LANDLOCK_ACCESS_FS_EXECUTE, rights, nil); err != nil {
			return -1, err
		}
	}
	for _, path := range readable(denied) {
		if err := allow(ruleset, path, readRights, rights, denied); err != nil {
			return -1, err
		}
	}
	// A read entry lies inside a denied directory, which is why it needs a rule of its own.
	for _, path := range p.Read {
		if err := allow(ruleset, path, readRights, rights, nil); err != nil {
			return -1, err
		}
	}
	if p.Net != nil {
		ports := map[uint16]uint64{}
		for _, port := range p.Net.Bind {
			ports[port] |= accessNetBindTCP
		}
		for _, port := range p.Net.Connect {
			ports[port] |= accessNetConnectTCP
		}
		for port, access := range ports {
			rule := netPortAttr{allowedAccess: access, port: uint64(port)}
			if _, _, errno := syscall.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), ruleNetPort,
				uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 { // #nosec G103 -- the kernel reads the struct during the call
				return -1, fmt.Errorf("sandbox: allow port %d: %w", port, errno)
			}
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return -1, fmt.Errorf("sandbox: no_new_privs: %w", err)
	}
	if _, _, errno := syscall.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return -1, fmt.Errorf("sandbox: restrict: %w", errno)
	}
	listen := listenAllowed
	switch {
	case supervised(p):
		listen = listenNotified
	case p.Net != nil:
		listen = listenDenied
	}
	return socketFilter(p.Net != nil, listen)
}

// The seccomp filter's return values and the offsets of struct seccomp_data it reads.
const (
	seccompAllow   = 0x7fff0000
	seccompErrno   = 0x00050000
	offsetSyscall  = 0
	offsetArch     = 4
	offsetArg0     = 16
	offsetArg1     = 24
	offsetArg2     = 32
	sockTypeMask   = 0xf  // SOCK_TYPE_MASK: the type without SOCK_NONBLOCK and SOCK_CLOEXEC
	bpfAnd         = 0x54 // BPF_ALU | BPF_AND | BPF_K
	x32SyscallBit  = 0x40000000
	bpfLoadWord    = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJumpEqual   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJumpGreater = 0x35 // BPF_JMP | BPF_JGE | BPF_K
	bpfReturn      = 0x06 // BPF_RET | BPF_K
)

// socketFilter installs a seccomp filter on the sockets the process may make.
//
// Always, no Unix-domain socket. Landlock does not mediate connecting to one. A process
// confined by Landlock alone reached the user's D-Bus and ran `systemd-run --user`, a command
// outside the sandbox, and the same road leads to the keyring, ssh-agent and a container daemon
// (docs/spikes/2026-11-landlock.md). seccomp cannot read the address a connect names, so the
// socket itself is refused: `socket(AF_UNIX)` fails with EACCES. A socketpair is not a
// socket() call and still works. `io_uring_setup` answers ENOSYS, since a ring can make a
// socket without the system call; a program falls back to ordinary calls. System calls of
// another architecture's table are refused whole, because their numbers mean something else.
//
// With net, the profile names a network, and Landlock's port rules are TCP's alone: a
// datagram left with every TCP port denied (the same spike). So the only sockets left are a
// TCP one of either Internet family, which Landlock then holds to its ports, and a netlink
// one of the routing protocol, which a resolver and `getifaddrs` open and which sends nothing
// off the machine. A datagram, a raw socket, a stream that is not TCP — MPTCP, SCTP — a
// netlink socket of any other protocol and every other family fail with EACCES.
//
// listen says what becomes of `listen`, which Landlock cannot: a socket that listens without
// a bind is given a port by the kernel, with no bind for a rule to refuse, and Landlock has no
// right for listen. A profile with a network and no port to bind is refused it, with EACCES.
// One that may bind has each call handed to its supervisor (supervise_linux.go), and the
// filter's notification descriptor is returned; otherwise -1 is.
func socketFilter(net bool, listen listenRule) (int, error) {
	denied := unix.SockFilter{Code: bpfReturn, K: seccompErrno | uint32(unix.EACCES)}
	allowed := unix.SockFilter{Code: bpfReturn, K: seccompAllow}
	filter := []unix.SockFilter{
		{Code: bpfLoadWord, K: offsetArch},
		{Code: bpfJumpEqual, Jt: 1, Jf: 0, K: auditArch},
		{Code: bpfReturn, K: seccompErrno | uint32(unix.EPERM)},
		{Code: bpfLoadWord, K: offsetSyscall},
		{Code: bpfJumpGreater, Jt: 0, Jf: 1, K: x32SyscallBit},
		{Code: bpfReturn, K: seccompErrno | uint32(unix.EPERM)},
		{Code: bpfJumpEqual, Jt: 0, Jf: 1, K: unix.SYS_IO_URING_SETUP},
		{Code: bpfReturn, K: seccompErrno | uint32(unix.ENOSYS)},
	}
	switch listen {
	case listenDenied:
		filter = append(filter, unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 1, K: unix.SYS_LISTEN}, denied)
	case listenNotified:
		// No notification descriptor of the process's own. The kernel asks the newest filter
		// that notifies, so a process with one would answer its own `listen` and tell the
		// kernel to go on — once its supervisor is gone, since only one descriptor exists at
		// a time. A refusal outranks a notification whatever filter is added later, and the
		// flag is a register value, which a filter can read.
		filter = append(filter,
			unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 4, K: unix.SYS_SECCOMP},
			unix.SockFilter{Code: bpfLoadWord, K: offsetArg1},
			unix.SockFilter{Code: bpfAnd, K: unix.SECCOMP_FILTER_FLAG_NEW_LISTENER},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 1, Jf: 0, K: 0},
			unix.SockFilter{Code: bpfReturn, K: seccompErrno | uint32(unix.EPERM)},
			unix.SockFilter{Code: bpfLoadWord, K: offsetSyscall},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 1, K: unix.SYS_LISTEN},
			unix.SockFilter{Code: bpfReturn, K: unix.SECCOMP_RET_USER_NOTIF})
	case listenAllowed:
	}
	filter = append(filter,
		unix.SockFilter{Code: bpfJumpEqual, Jt: 1, Jf: 0, K: unix.SYS_SOCKET},
		allowed,
		unix.SockFilter{Code: bpfLoadWord, K: offsetArg0},
		unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 1, K: unix.AF_UNIX},
		denied,
	)
	if !net {
		filter = append(filter, allowed)
	} else {
		// The jumps are counted from the instruction after each. A netlink socket is decided
		// on its protocol at once; the Internet families' checks end at `denied` and
		// `allowed`, the last two.
		filter = append(filter,
			unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 4, K: unix.AF_NETLINK},
			unix.SockFilter{Code: bpfLoadWord, K: offsetArg2},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 1, K: unix.NETLINK_ROUTE},
			allowed,
			denied,
			unix.SockFilter{Code: bpfJumpEqual, Jt: 1, Jf: 0, K: unix.AF_INET},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 6, K: unix.AF_INET6},
			unix.SockFilter{Code: bpfLoadWord, K: offsetArg1},
			unix.SockFilter{Code: bpfAnd, K: sockTypeMask},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 0, Jf: 3, K: unix.SOCK_STREAM},
			unix.SockFilter{Code: bpfLoadWord, K: offsetArg2},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 2, Jf: 0, K: 0},
			unix.SockFilter{Code: bpfJumpEqual, Jt: 1, Jf: 0, K: unix.IPPROTO_TCP},
			denied,
			allowed,
		)
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]} // #nosec G115 -- a few dozen instructions
	flags := uintptr(0)
	if listen == listenNotified {
		flags = unix.SECCOMP_FILTER_FLAG_NEW_LISTENER
	}
	fd, _, errno := syscall.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, flags, uintptr(unsafe.Pointer(&prog))) // #nosec G103 -- the kernel reads the program during the call
	if errno != 0 {
		return -1, fmt.Errorf("sandbox: seccomp: %w", errno)
	}
	if listen != listenNotified {
		return -1, nil
	}
	return int(fd), nil
}

// listenRule is what the seccomp filter does with `listen`.
type listenRule int

const (
	listenAllowed listenRule = iota
	listenDenied
	listenNotified
)

// readable are the paths that make "everything except deny" out of allow rules, which is all
// Landlock has: every entry beside each denied directory and beside each of its ancestors, up
// to the root. With nothing denied it is the root.
//
// Two things follow, and the delta records both. A file created later directly in an ancestor
// of a denied directory has no rule and is not readable; and the ancestors themselves are not
// listable, because a listing right on one would reach the denied directory beneath it.
func readable(deny []string) []string {
	if len(deny) == 0 {
		return []string{"/"}
	}
	denied := map[string]bool{}
	walk := map[string]bool{}
	for _, d := range deny {
		denied[d] = true
		for a := filepath.Dir(d); ; a = filepath.Dir(a) {
			walk[a] = true
			if a == "/" {
				break
			}
		}
	}
	var out []string
	for dir := range walk {
		if denied[dir] {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // unreadable to the daemon is unreadable to what it confines
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			// A symbolic link gets no rule: one pointing at a denied directory, or above it,
			// would open it. Following a link needs no right, and its target has its own rule
			// wherever it is readable.
			if denied[path] || walk[path] || e.Type()&os.ModeSymlink != 0 {
				continue
			}
			out = append(out, path)
		}
	}
	return out
}

// resolve follows symbolic links in a path that exists, so a rule lands on the directory the
// kernel will see; one that does not exist yet is taken as written.
func resolve(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// allow adds one rule. A path that does not exist grants nothing and is no error: a profile
// names directories a tool may never have made, and a sibling can vanish between the listing
// and the rule. The path is never followed through a symbolic link at its end, and with denied
// given the rule is dropped when what was opened is, holds or lies inside a denied directory —
// an entry swapped for a link between the listing and here.
func allow(ruleset int, path string, rights, handledRights uint64, denied []string) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ELOOP) {
			return nil
		}
		return fmt.Errorf("sandbox: open %s: %w", path, err)
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("sandbox: stat %s: %w", path, err)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return nil
	case unix.S_IFDIR:
	default:
		rights &= fileRights
	}
	if len(denied) > 0 {
		real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil {
			return nil // what cannot be named cannot be shown not to be denied
		}
		for _, d := range denied {
			if domain.Within(real, d) || domain.Within(d, real) {
				return nil
			}
		}
	}
	rights &= handledRights
	if rights == 0 {
		return nil
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: rights, Parent_fd: int32(fd)}
	if _, _, errno := syscall.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 { // #nosec G103 -- the kernel reads the struct during the call
		return fmt.Errorf("sandbox: allow %s: %w", path, errno)
	}
	return nil
}
