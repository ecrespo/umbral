//go:build linux

package landlock_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ecrespo/umbral/internal/sandbox/adapters/landlock"
	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// TestMain makes the test binary its own helper, as umbrald is the daemon's: run with
// HelperFlag it confines itself and execs the command.
func TestMain(m *testing.M) {
	if len(os.Args) > 3 && os.Args[1] == landlock.HelperFlag && os.Args[3] == "--" {
		err := landlock.Exec(os.Args[2], os.Args[4:])
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	if len(os.Args) == 2 && os.Args[1] == probeSockets {
		// What a confined process is refused, tried from inside: a Unix-domain socket and an
		// io_uring. A socketpair and an Internet socket are what it keeps.
		_, unixErr := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		_, _, ringErr := unix.Syscall(unix.SYS_IO_URING_SETUP, 4, 0, 0)
		_, pairErr := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		_, inetErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		fmt.Printf("unix=%v ring=%v pair=%v inet=%v\n", unixErr, ringErr, pairErr, inetErr)
		os.Exit(0)
	}
	if len(os.Args) == 6 && os.Args[1] == probeNet {
		probeNetwork(os.Args[2:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// probeNet makes the test binary report what it can do on the network: its arguments are a
// port it may connect to, one it may not, one it may bind and one it may not.
const probeNet = "--probe-net"

func probeNetwork(args []string) {
	port := func(s string) int {
		n, _ := strconv.Atoi(s)
		return n
	}
	loopback := func(p int) unix.Sockaddr { return &unix.SockaddrInet4{Port: p, Addr: [4]byte{127, 0, 0, 1}} }
	tcp := func() int {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			fmt.Printf("tcp-socket=%v\n", err)
			os.Exit(0)
		}
		return fd
	}
	_, udpErr := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	_, udp6Err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK, 0)
	_, rawErr := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_ICMP)
	// A stream socket that is not TCP: Landlock's port rules are TCP's alone.
	_, mptcpErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 262)
	_, packetErr := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM, 0)
	_, netlinkErr := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	// Another netlink protocol: this one reads every socket on the machine.
	_, diagErr := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_SOCK_DIAG)
	// The other Internet family: the rules are by port, whatever the address.
	loopback6 := func(p int) unix.Sockaddr {
		return &unix.SockaddrInet6{Port: p, Addr: [16]byte{15: 1}}
	}
	tcp6 := func() (int, error) { return unix.Socket(unix.AF_INET6, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0) }
	connect6 := func(p int) error {
		fd, err := tcp6()
		if err != nil {
			return err
		}
		return unix.Connect(fd, loopback6(p))
	}
	connect6OK, connect6No := connect6(port(args[0])), connect6(port(args[1]))
	_, pairErr := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	// A port's two rights are separate: the one it may connect to is not one it may listen
	// on, and the reverse. Tried before the bind below takes its port.
	crossConnect := unix.Connect(tcp(), loopback(port(args[2])))
	crossBind := unix.Bind(tcp(), loopback(port(args[0])))
	connectOK := unix.Connect(tcp(), loopback(port(args[0])))
	connectNo := unix.Connect(tcp(), loopback(port(args[1])))
	bindOK := unix.Bind(tcp(), loopback(port(args[2])))
	bindNo := unix.Bind(tcp(), loopback(port(args[3])))
	bindAny := unix.Bind(tcp(), loopback(0))
	// A socket that listens without a bind is given a port by the kernel, with no bind for a
	// rule to refuse.
	listenUnbound := unix.Listen(tcp(), 1)
	// TCP Fast Open connects inside a send, with no connect for a rule to refuse.
	fastOpen := unix.Sendto(tcp(), []byte("x"), unix.MSG_FASTOPEN, loopback(port(args[1])))
	fmt.Printf("udp=%v udp6=%v raw=%v mptcp=%v packet=%v netlink=%v pair=%v connect-ok=%v connect-no=%v bind-ok=%v bind-no=%v bind-any=%v connect-to-bind-port=%v bind-on-connect-port=%v listen-unbound=%v fast-open=%v netlink-diag=%v connect6-ok=%v connect6-no=%v\n",
		udpErr, udp6Err, rawErr, mptcpErr, packetErr, netlinkErr, pairErr, connectOK, connectNo, bindOK, bindNo, bindAny, crossConnect, crossBind, listenUnbound, fastOpen, diagErr, connect6OK, connect6No)
}

// probeSockets makes the test binary report which sockets it can make.
const probeSockets = "--probe-sockets"

// bench is Umbral's layout in small: a data directory holding a database and two threads'
// worktrees, a runtime directory holding the token and the agent's root, and the repository's
// main copy outside both.
type bench struct {
	data, run, main, mine, sibling, root string
	sb                                   *landlock.Sandbox
}

func newBench(t *testing.T) bench {
	t.Helper()
	sb := landlock.New(os.Args[0])
	if st := sb.Status(); !st.Enforcing() {
		// Where the sandbox is required (CI's Linux job) a system that cannot confine is a
		// failure: a skip would leave the kernel's refusals unproven behind a green run.
		if os.Getenv("UMBRAL_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("UMBRAL_REQUIRE_SANDBOX is set and this system cannot confine: %+v", st)
		}
		t.Skipf("no sandbox on this system: %+v", st)
	}
	// Resolved, as the daemon resolves every path it puts in a profile.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := bench{
		data: filepath.Join(base, "data"), run: filepath.Join(base, "run"), main: filepath.Join(base, "repo"), sb: sb,
	}
	b.mine = filepath.Join(b.data, "worktrees", "thr_mine")
	b.sibling = filepath.Join(b.data, "worktrees", "thr_other")
	b.root = filepath.Join(b.run, "acp", "thr_mine")
	for _, d := range []string{b.mine, b.sibling, b.root, b.main, filepath.Join(b.run, "acp", "thr_other")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, text := range map[string]string{
		filepath.Join(b.data, "umbral.db"):                    "the database",
		filepath.Join(b.run, "token"):                         "the token",
		filepath.Join(b.sibling, "secret.txt"):                "a sibling's work",
		filepath.Join(b.run, "acp", "thr_other", "auth.json"): "another agent's credentials",
		filepath.Join(b.main, "README"):                       "the main copy",
		filepath.Join(b.mine, "mine.txt"):                     "my work",
	} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

// profile is the thread's profile, as the daemon makes it.
func (b bench) profile(t *testing.T) domain.Profile {
	t.Helper()
	p, err := domain.AgentProfile(domain.Agent{WorkDir: b.mine, OwnWorktree: true, Root: b.root, Protected: []string{b.data, b.run}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sh runs a shell line confined to p, in the thread's worktree.
func (b bench) sh(t *testing.T, p domain.Profile, line string) (string, error) {
	t.Helper()
	argv, err := b.sb.Command(p, []string{"/bin/sh", "-c", line})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	cmd.Dir = b.mine
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAConfinedProcessWritesOnlyInsideItsProfile_REQ_SBX_001: the kernel refuses a write
// outside the thread's directory and the agent's root — the main copy, a sibling's worktree,
// the user's home — and allows one inside either.
func TestAConfinedProcessWritesOnlyInsideItsProfile_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"the main copy":        filepath.Join(b.main, "planted"),
		"a sibling's worktree": filepath.Join(b.sibling, "planted"),
		"the data directory":   filepath.Join(b.data, "planted"),
		"the user's home":      filepath.Join(home, ".umbral-sandbox-test-planted"),
		"an existing file":     filepath.Join(b.main, "README"),
	} {
		if out, err := b.sh(t, p, "echo x > "+target); err == nil {
			_ = os.Remove(target)
			t.Errorf("%s was written: %s", name, out)
		} else if !strings.Contains(out, "Permission denied") {
			t.Errorf("%s: refused by something other than the kernel: %v %s", name, err, out)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(b.main, "README")); string(got) != "the main copy" {
		t.Errorf("the main copy's file changed: %q", got)
	}
	for name, target := range map[string]string{
		"the worktree":       filepath.Join(b.mine, "made"),
		"a new directory in": filepath.Join(b.mine, "sub", "made"),
		"the agent's root":   filepath.Join(b.root, "made"),
	} {
		if out, err := b.sh(t, p, "mkdir -p "+filepath.Dir(target)+" && echo x > "+target+" && rm "+target); err != nil {
			t.Errorf("%s was refused: %v %s", name, err, out)
		}
	}
}

// TestAConfinedProcessWritesItsDeviceFiles_REQ_SBX_001: a redirect to /dev/null works; without
// the device files every shell and git fail at their first one.
func TestAConfinedProcessWritesItsDeviceFiles_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	if out, err := b.sh(t, b.profile(t), "echo x > /dev/null && head -c 4 /dev/zero > /dev/null && echo ran"); err != nil || !strings.Contains(out, "ran") {
		t.Fatalf("the device files were refused: %v %s", err, out)
	}
}

// TestAConfinedProcessCannotReadUmbralsDirectories_REQ_SBX_002: the kernel refuses reading
// the database, the token, a sibling's worktree and another agent's home, and listing them;
// the main copy, the system and the thread's own worktree — which lies inside the data
// directory — stay readable.
func TestAConfinedProcessCannotReadUmbralsDirectories_REQ_SBX_002(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	for name, line := range map[string]string{
		"the database":         "cat " + filepath.Join(b.data, "umbral.db"),
		"the token":            "cat " + filepath.Join(b.run, "token"),
		"a sibling's worktree": "cat " + filepath.Join(b.sibling, "secret.txt"),
		"another agent's home": "cat " + filepath.Join(b.run, "acp", "thr_other", "auth.json"),
		"the worktrees' names": "ls " + filepath.Join(b.data, "worktrees"),
		"a sibling's listing":  "ls " + b.sibling,
	} {
		out, err := b.sh(t, p, line)
		if err == nil {
			t.Errorf("%s was read: %s", name, out)
		} else if !strings.Contains(out, "Permission denied") {
			t.Errorf("%s: refused by something other than the kernel: %v %s", name, err, out)
		}
	}
	for name, line := range map[string]string{
		"the main copy":    "cat " + filepath.Join(b.main, "README"),
		"its own worktree": "cat mine.txt && ls . >/dev/null",
		"the system":       "cat /etc/hostname >/dev/null && ls /usr/bin >/dev/null",
	} {
		if out, err := b.sh(t, p, line); err != nil {
			t.Errorf("%s was refused: %v %s", name, err, out)
		}
	}
}

// TestAConfinedProcessWritesNothingInTheRepositorysGitDirectory_REQ_SBX_001: a worktree's
// commit writes into the main copy's `.git` — the worktree's record, the objects, the refs —
// and the daemon's own git reads all three when it lands the work. None is the agent's to
// write: a commit, a staged file, a moved branch, a planted object, a replaced object, a
// rewritten `commondir`, a hook and the configuration are all refused, and the worktree's
// files, which are what lands, are written.
func TestAConfinedProcessWritesNothingInTheRepositorysGitDirectory_REQ_SBX_001(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	b := newBench(t)
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(b.main, "init", "-q", "-b", "main")
	git(b.main, "add", "-A")
	git(b.main, "commit", "-q", "-m", "init")
	if err := os.RemoveAll(b.mine); err != nil {
		t.Fatal(err)
	}
	git(b.main, "worktree", "add", "-q", "-b", "umbral/thr_mine", b.mine)
	head := git(b.main, "rev-parse", "main")
	p := b.profile(t)

	if out, err := b.sh(t, p, "echo work > work.txt && cat work.txt"); err != nil || !strings.Contains(out, "work") {
		t.Fatalf("the worktree's own file was not written: %v %s", err, out)
	}
	gitDir := filepath.Join(b.main, ".git")
	admin := filepath.Join(gitDir, "worktrees", "thr_mine")
	setup := "export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null; "
	for name, line := range map[string]string{
		"staging a file":              setup + "git add -A",
		"a commit":                    setup + "git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m work",
		"moving a branch":             setup + "git update-ref refs/heads/main " + head,
		"a replace ref":               "mkdir -p " + filepath.Join(gitDir, "refs", "replace") + " && echo " + head + " > " + filepath.Join(gitDir, "refs", "replace", head),
		"an object":                   setup + "echo planted | git hash-object -w --stdin",
		"an object file":              "echo x > " + filepath.Join(gitDir, "objects", "planted"),
		"the record's commondir":      "echo /elsewhere > " + filepath.Join(admin, "commondir"),
		"the record's gitdir":         "echo /elsewhere/.git > " + filepath.Join(admin, "gitdir"),
		"the record's HEAD":           "echo garbage > " + filepath.Join(admin, "HEAD"),
		"a worktree configuration":    "echo '[core]' > " + filepath.Join(admin, "config.worktree"),
		"a hook":                      "echo x >> " + filepath.Join(gitDir, "hooks", "pre-commit"),
		"the main copy's config":      "echo x >> " + filepath.Join(gitDir, "config"),
		"the main copy's HEAD":        "echo x >> " + filepath.Join(gitDir, "HEAD"),
		"the main copy's file":        "echo x >> " + filepath.Join(b.main, "README"),
		"the main copy's reflog":      "echo x >> " + filepath.Join(gitDir, "logs", "HEAD"),
		"the main copy's packed-refs": "echo x >> " + filepath.Join(gitDir, "packed-refs"),
	} {
		if out, err := b.sh(t, p, line); err == nil {
			t.Errorf("%s was not refused: %s", name, out)
		}
	}
	if got := git(b.main, "rev-parse", "umbral/thr_mine"); got != head {
		t.Errorf("the thread's branch moved to %s", got)
	}
	// Reading stays: the agent sees its branch, its changes and the history.
	if out, err := b.sh(t, p, setup+"git status --porcelain && git log --format=%s -1"); err != nil || !strings.Contains(out, "work.txt") || !strings.Contains(out, "init") {
		t.Errorf("git status and log under the sandbox: %v %s", err, out)
	}
}

// TestAReadEntryIsReadAndNotWritten_REQ_SBX_006: a read entry inside a denied directory — a
// shell's start-up files in the runtime directory — is read and run from, and neither written
// nor a way to its neighbours.
func TestAReadEntryIsReadAndNotWritten_REQ_SBX_006(t *testing.T) {
	b := newBench(t)
	boot := filepath.Join(b.run, "shellinteg-1")
	if err := os.MkdirAll(boot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boot, "rc"), []byte("echo sourced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := b.profile(t)
	if out, err := b.sh(t, p, "cat "+filepath.Join(boot, "rc")); err == nil {
		t.Fatalf("without a read entry the start-up file is read: %s", out)
	}
	p.Read = []string{boot}
	if out, err := b.sh(t, p, ". "+filepath.Join(boot, "rc")+" && ls "+boot); err != nil || !strings.Contains(out, "sourced") {
		t.Errorf("the read entry is not readable: %v %s", err, out)
	}
	for name, line := range map[string]string{
		"writing the start-up file": "echo x >> " + filepath.Join(boot, "rc"),
		"creating beside it":        "echo x > " + filepath.Join(boot, "planted"),
		"the token beside it":       "cat " + filepath.Join(b.run, "token"),
		"another agent's home":      "cat " + filepath.Join(b.run, "acp", "thr_other", "auth.json"),
		"listing the runtime dir":   "ls " + b.run,
	} {
		if out, err := b.sh(t, p, line); err == nil {
			t.Errorf("%s was not refused: %s", name, out)
		}
	}
}

// TestALinkBesideADeniedDirectoryOpensNothing_REQ_SBX_002: the read list is made of what lies
// beside each denied directory, and a symbolic link there — to the data directory itself, to a
// directory above it — gets no rule, so it opens nothing the profile denies. A link to a
// readable place still reads, since its target has a rule of its own.
func TestALinkBesideADeniedDirectoryOpensNothing_REQ_SBX_002(t *testing.T) {
	b := newBench(t)
	base := filepath.Dir(b.data)
	for name, target := range map[string]string{"alias": b.data, "above": base, "run-alias": b.run, "ok": b.main} {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	p := b.profile(t)
	for name, line := range map[string]string{
		"a link to the data directory":     "cat " + filepath.Join(base, "alias", "umbral.db"),
		"a link to the directory above it": "cat " + filepath.Join(base, "above", "data", "umbral.db"),
		"a link to the runtime directory":  "cat " + filepath.Join(base, "run-alias", "token"),
		"a sibling through the first link": "cat " + filepath.Join(base, "alias", "worktrees", "thr_other", "secret.txt"),
	} {
		if out, err := b.sh(t, p, line); err == nil {
			t.Errorf("%s was read: %s", name, out)
		}
	}
	if out, err := b.sh(t, p, "cat "+filepath.Join(base, "ok", "README")); err != nil {
		t.Errorf("a link to the main copy was refused: %v %s", err, out)
	}
}

// TestAConfinedProcessMakesNoLocalSocket_REQ_SBX_001: Landlock does not mediate a connection
// to a Unix-domain socket, and through the user's D-Bus a process confined by it alone ran a
// command outside the sandbox. The process is refused the socket itself, and an io_uring, which
// can make one; a socketpair and an Internet socket still work.
func TestAConfinedProcessMakesNoLocalSocket_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	argv, err := b.sb.Command(b.profile(t), []string{os.Args[0], probeSockets})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("the probe did not run: %v %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "unix=permission denied ring=function not implemented pair=<nil> inet=<nil>"; got != want {
		t.Errorf("confined, the probe says %q, want %q", got, want)
	}
	plain, err := exec.CommandContext(t.Context(), os.Args[0], probeSockets).CombinedOutput()
	if err != nil || !strings.HasPrefix(string(plain), "unix=<nil> ") {
		t.Fatalf("unconfined, the probe says %q (%v): the test proves nothing here", plain, err)
	}
}

// TestAProfileThatCannotBeAppliedRunsNothing_REQ_SBX_001: a profile the helper cannot read is
// a failure, never a reason to run the command unconfined.
func TestAProfileThatCannotBeAppliedRunsNothing_REQ_SBX_001(t *testing.T) {
	newBench(t)
	for name, arg := range map[string]string{"not JSON": "{", "a relative path": `{"write":["x"]}`, "an unknown member": `{"network":"all"}`} {
		// The command only prints: it would succeed under any profile, so it is the helper's
		// refusal that keeps it from running.
		cmd := exec.CommandContext(t.Context(), os.Args[0], landlock.HelperFlag, arg, "--", "/bin/sh", "-c", "echo the-command-ran")
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "the-command-ran") {
			t.Errorf("%s: the helper ran its command: %v %s", name, err, out)
		}
	}
}

// TestTheAdapterSaysWhatItEnforces_REQ_SBX_003: on a kernel with Landlock the status names the
// mechanism; an adapter with no helper to run cannot confine and says so, returning the
// command as it came.
func TestTheAdapterSaysWhatItEnforces_REQ_SBX_003(t *testing.T) {
	b := newBench(t)
	if st := b.sb.Status(); st.State != domain.StateEnforcing || st.Mechanism != landlock.Mechanism || st.Reason != "" {
		t.Fatalf("Status = %+v", st)
	}
	none := landlock.New()
	if st := none.Status(); st.Enforcing() || st.Reason == "" {
		t.Fatalf("an adapter with no helper: %+v", st)
	}
	argv, err := none.Command(b.profile(t), []string{"true"})
	if err != nil || len(argv) != 1 || argv[0] != "true" {
		t.Fatalf("Command without a sandbox = %v, %v", argv, err)
	}
}

// listener is a TCP listener on a loopback port of the kernel's choosing, and that port.
func listener(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// freePort is a loopback port nothing listens on now.
func freePort(t *testing.T) string {
	t.Helper()
	ln, port := listener(t)
	_ = ln.Close()
	return port
}

// TestAConfinedProcessReachesOnlyItsPorts_REQ_SBX_008: a profile that names a network leaves
// the process TCP alone, and of TCP only a connection to the ports it may connect to and a
// bind on the ports it may bind — no other port, and no port of the kernel's choosing. Landlock
// has no rule for anything but TCP, so every other Internet socket is refused when it is made:
// a datagram, a raw one, and a stream that is not TCP. Without a network in the profile the
// same process reaches all of it.
func TestAConfinedProcessReachesOnlyItsPorts_REQ_SBX_008(t *testing.T) {
	b := newBench(t)
	if !b.sb.Status().Network {
		if os.Getenv("UMBRAL_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("UMBRAL_REQUIRE_SANDBOX is set and this kernel cannot confine the network: %+v", b.sb.Status())
		}
		t.Skipf("this kernel's Landlock has no network rules: %+v", b.sb.Status())
	}
	_, allowed := listener(t)
	_, other := listener(t)
	bindOK, bindNo := freePort(t), freePort(t)
	port := func(s string) uint16 {
		n, err := strconv.ParseUint(s, 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		return uint16(n)
	}
	run := func(p domain.Profile) string {
		t.Helper()
		argv, err := b.sb.Command(p, []string{os.Args[0], probeNet, allowed, other, bindOK, bindNo})
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("the probe did not run: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}

	p := b.profile(t)
	p.Net = &domain.Net{Connect: []uint16{port(allowed)}, Bind: []uint16{port(bindOK)}}
	const denied = "permission denied"
	want := "udp=" + denied + " udp6=" + denied + " raw=" + denied + " mptcp=" + denied + " packet=" + denied +
		" netlink=<nil> pair=<nil> connect-ok=<nil> connect-no=" + denied + " bind-ok=<nil> bind-no=" + denied + " bind-any=" + denied +
		" connect-to-bind-port=" + denied + " bind-on-connect-port=" + denied
	// The limit the delta records: a process that may bind keeps `listen`, and a socket that
	// listens without a bind is given a port by the kernel. Landlock has no right for it.
	// Should a kernel refuse it one day, this line says so.
	// Nothing listens on the IPv6 loopback: an allowed port answers "connection refused", the
	// peer's answer, and a refused one "permission denied", the kernel's, before any peer.
	want += " listen-unbound=<nil> fast-open=" + denied + " netlink-diag=" + denied + " connect6-ok=connection refused connect6-no=" + denied
	if got := run(p); got != want {
		t.Errorf("with a network profile the probe says\n %q, want\n %q", got, want)
	}
	// A profile with no port to bind has no use for `listen`, and is refused it.
	p.Net = &domain.Net{Connect: []uint16{port(allowed)}}
	if got := run(p); !strings.Contains(got, "connect-ok=<nil>") || !strings.Contains(got, "bind-ok="+denied) || !strings.Contains(got, "listen-unbound="+denied) {
		t.Errorf("with nothing to bind the probe says %q, want its connection allowed and no bind or listen", got)
	}

	// The same probe with no network in its profile: the test proves nothing unless what was
	// refused above works here.
	plain := run(b.profile(t))
	for _, open := range []string{"udp=<nil>", "udp6=<nil>", "connect-ok=<nil>", "connect-no=<nil>", "bind-ok=<nil>", "bind-no=<nil>", "bind-any=<nil>", "listen-unbound=<nil>", "fast-open=<nil>", "netlink-diag=<nil>", "connect6-no=connection refused"} {
		if !strings.Contains(plain, open) {
			t.Errorf("without a network profile the probe says %q, want %s", plain, open)
		}
	}
}

// TestANetworkProfileCannotNameEveryPort_REQ_SBX_008: a port of zero is the kernel's choice of
// port, which is every port. The adapter refuses a profile that names it, and so does the
// helper, so one handed to it directly runs nothing.
func TestANetworkProfileCannotNameEveryPort_REQ_SBX_008(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	p.Net = &domain.Net{Connect: []uint16{0}}
	if _, err := b.sb.Command(p, []string{"true"}); err == nil {
		t.Error("a profile that connects to port 0 was accepted")
	}
	p.Net = &domain.Net{Bind: []uint16{0}}
	if _, err := b.sb.Command(p, []string{"true"}); err == nil {
		t.Error("a profile that binds port 0 was accepted")
	}
	// The helper refuses the same, so a profile handed to it directly runs nothing.
	cmd := exec.CommandContext(t.Context(), os.Args[0], landlock.HelperFlag, `{"net":{"bind":[0]}}`, "--", "/bin/sh", "-c", "echo the-command-ran")
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "the-command-ran") {
		t.Errorf("the helper ran its command under a network it cannot apply: %v %s", err, out)
	}
}
