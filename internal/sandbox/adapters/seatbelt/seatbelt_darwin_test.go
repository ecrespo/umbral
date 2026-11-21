package seatbelt

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// These tests run a real command under the profile, against the kernel. They are the ones
// T-HRD-15 runs against Landlock, through this adapter (T-HRD-16's Done line).

// bench is a thread's surroundings on disk: Umbral's data and runtime directories, with this
// thread's worktree and root and a sibling's inside them, and a repository's main copy.
type bench struct {
	data, run, mine, sibling, root, main string
	sb                                   *Sandbox
}

func newBench(t *testing.T) bench {
	t.Helper()
	sb := New()
	if st := sb.Status(); !st.Enforcing() {
		// Where the sandbox is required (CI's macOS job) a system that cannot confine is a
		// failure: a skip would leave the kernel's refusals unproven behind a green run.
		if os.Getenv("UMBRAL_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("UMBRAL_REQUIRE_SANDBOX is set and this system cannot confine: %+v", st)
		}
		t.Skipf("no sandbox on this system: %+v", st)
	}
	// Resolved, as the daemon resolves every path it puts in a profile: a rule written with
	// `/var/…` does not match the `/private/var/…` the kernel sees.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := bench{data: filepath.Join(base, "data"), run: filepath.Join(base, "run"), main: filepath.Join(base, "repo"), sb: sb}
	b.mine = filepath.Join(b.data, "worktrees", "r", "thr_mine")
	b.sibling = filepath.Join(b.data, "worktrees", "r", "thr_other")
	b.root = filepath.Join(b.run, "acp", "thr_mine")
	for _, d := range []string{b.mine, b.sibling, filepath.Join(b.root, "tmp"), filepath.Join(b.main, ".git", "objects"), filepath.Join(b.run, "acp", "thr_other")} {
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

func (b bench) profile(t *testing.T) domain.Profile {
	t.Helper()
	p, err := domain.AgentProfile(domain.Agent{WorkDir: b.mine, OwnWorktree: true, Root: b.root, Protected: []string{b.data, b.run}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sh runs a shell line confined to p, in the thread's worktree, with the temporary directory
// the daemon gives a hosted agent.
func (b bench) sh(t *testing.T, p domain.Profile, line string) (string, error) {
	t.Helper()
	argv, err := b.sb.Command(p, []string{"/bin/sh", "-c", line})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	cmd.Dir = b.mine
	cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(b.root, "tmp"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAConfinedProcessWritesOnlyInsideItsProfile_REQ_SBX_001: the kernel refuses a write
// outside the thread's directory and the agent's root — the main copy, its `.git`, a sibling's
// worktree, the user's home — and allows one inside either. A process the command starts is
// confined with it.
func TestAConfinedProcessWritesOnlyInsideItsProfile_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]string{
		"the main copy":            filepath.Join(b.main, "planted"),
		"the main copy's objects":  filepath.Join(b.main, ".git", "objects", "planted"),
		"a sibling's worktree":     filepath.Join(b.sibling, "planted"),
		"Umbral's data directory":  filepath.Join(b.data, "planted"),
		"another agent's home":     filepath.Join(b.run, "acp", "thr_other", "planted"),
		"the user's home":          filepath.Join(home, ".umbral-sandbox-test-planted"),
		"the directory above mine": filepath.Join(filepath.Dir(b.mine), "planted"),
	}
	for name, target := range refused {
		if out, err := b.sh(t, p, "echo x > "+target); err == nil {
			t.Errorf("%s was written: %s", name, out)
		}
		if out, err := b.sh(t, p, "/bin/sh -c 'echo x > "+target+"'"); err == nil {
			t.Errorf("%s was written by a child process: %s", name, out)
		}
		if _, err := os.Lstat(target); err == nil {
			t.Errorf("%s exists after the refusal", name)
			_ = os.Remove(target)
		}
	}
	if out, err := b.sh(t, p, "/bin/chmod 644 "+filepath.Join(b.main, "README")); err == nil {
		t.Errorf("the main copy's file changed mode: %s", out)
	}
	for name, target := range map[string]string{
		"the worktree":     filepath.Join(b.mine, "made.txt"),
		"the agent's root": filepath.Join(b.root, "made.txt"),
		"its TMPDIR":       filepath.Join(b.root, "tmp", "made.txt"),
	} {
		if out, err := b.sh(t, p, "echo x > "+target+" && cat "+target); err != nil {
			t.Errorf("%s was refused: %v %s", name, err, out)
		}
	}
	if out, err := b.sh(t, p, "cat "+filepath.Join(b.main, "README")); err != nil || !strings.Contains(out, "the main copy") {
		t.Errorf("the main copy is not readable: %v %s", err, out)
	}
}

// TestAConfinedProcessWritesItsDeviceFiles_REQ_SBX_001: a shell's redirect to /dev/null works,
// and the tools Apple ships as launchers of the developer tools start, which need a writable
// temporary directory.
func TestAConfinedProcessWritesItsDeviceFiles_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	// `mktemp -t` is not among them: it writes to the user's own temporary directory whatever
	// TMPDIR says, and that directory is every program's of the user's, so it stays refused.
	for _, line := range []string{"echo x > /dev/null", "cat /dev/zero | head -c 1 > /dev/null", "/usr/bin/mktemp " + filepath.Join(b.root, "tmp", "umbral.XXXXXX") + " >/dev/null"} {
		if out, err := b.sh(t, p, line); err != nil {
			t.Errorf("%q was refused: %v %s", line, err, out)
		}
	}
	if _, err := os.Stat("/usr/bin/git"); err == nil {
		if out, err := b.sh(t, p, "/usr/bin/git --version"); err != nil {
			t.Errorf("/usr/bin/git does not start under the sandbox: %v %s", err, out)
		}
	}
}

// TestAConfinedProcessCannotReadUmbralsDirectories_REQ_SBX_002: the database, the token, a
// sibling's worktree and another agent's home are neither read, listed nor probed with stat.
// The thread's own worktree and root, inside those directories, are read, and the path to
// them resolves.
func TestAConfinedProcessCannotReadUmbralsDirectories_REQ_SBX_002(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	for name, line := range map[string]string{
		"the database":               "cat " + filepath.Join(b.data, "umbral.db"),
		"the token":                  "cat " + filepath.Join(b.run, "token"),
		"a sibling's file":           "cat " + filepath.Join(b.sibling, "secret.txt"),
		"a sibling's listing":        "ls " + b.sibling,
		"the worktrees' listing":     "ls " + filepath.Dir(b.mine),
		"the data directory listing": "ls " + b.data,
		"another agent's file":       "cat " + filepath.Join(b.run, "acp", "thr_other", "auth.json"),
		"stat of a sibling's file":   "/usr/bin/stat " + filepath.Join(b.sibling, "secret.txt"),
		"stat of the database":       "/usr/bin/stat " + filepath.Join(b.data, "umbral.db"),
	} {
		if out, err := b.sh(t, p, line); err == nil {
			t.Errorf("%s answered: %s", name, out)
		}
	}
	for name, line := range map[string]string{
		"my own file":        "cat mine.txt",
		"my own listing":     "ls " + b.mine,
		"the resolved cwd":   "pwd -P",
		"realpath of mine":   "/bin/realpath " + filepath.Join(b.mine, "mine.txt"),
		"my root's listing":  "ls " + b.root,
		"a file in my root":  "echo x > " + filepath.Join(b.root, "f") + " && cat " + filepath.Join(b.root, "f"),
		"a shell started in": "/bin/zsh -c 'cd " + b.mine + " && ls >/dev/null'",
	} {
		if out, err := b.sh(t, p, line); err != nil {
			t.Errorf("%s was refused: %v %s", name, err, out)
		}
	}
	// A link in the worktree to a denied file opens nothing: the rule is on the file reached.
	if err := os.Symlink(filepath.Join(b.data, "umbral.db"), filepath.Join(b.mine, "link")); err != nil {
		t.Fatal(err)
	}
	if out, err := b.sh(t, p, "cat link"); err == nil {
		t.Errorf("the database was read through a link in the worktree: %s", out)
	}
}

// TestAConfinedProcessReachesNoLocalSocket_REQ_SBX_001: a connection to another process's
// Unix-domain socket is refused — through one, on Linux, a confined process had the user's
// session services run a command outside the sandbox. A pair of its own still works.
func TestAConfinedProcessReachesNoLocalSocket_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	// A short path: a socket's is limited to 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "umb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("reached\n"))
			_ = c.Close()
		}
	}()
	line := "/usr/bin/nc -U " + sock + " </dev/null"
	// The same line outside the sandbox reaches the listener: the refusal below is the
	// sandbox's and not a broken probe.
	plain := exec.CommandContext(t.Context(), "/bin/sh", "-c", line)
	if out, err := plain.CombinedOutput(); err != nil || !strings.Contains(string(out), "reached") {
		t.Fatalf("the probe does not reach the socket even unconfined: %v %s", err, out)
	}
	if out, err := b.sh(t, p, line); err == nil || strings.Contains(out, "reached") {
		t.Errorf("a confined process reached another process's socket: %v %s", err, out)
	}
	if out, err := b.sh(t, p, "echo piped | cat"); err != nil || !strings.Contains(out, "piped") {
		t.Errorf("a pipe of its own was refused: %v %s", err, out)
	}
}

// TestAConfinedProcessStartsNothingOutsideTheSandbox_REQ_SBX_001: launchd starts a job as the
// user, outside any sandbox. A confined process cannot hand it one: the job's file is never
// made.
func TestAConfinedProcessStartsNothingOutsideTheSandbox_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	escaped := filepath.Join(b.main, "escaped")
	label := "io.github.ecrespo.umbral.sandbox-test"
	t.Cleanup(func() { _ = exec.Command("/bin/launchctl", "remove", label).Run() })
	out, _ := b.sh(t, p, "/bin/launchctl submit -l "+label+" -- /usr/bin/touch "+escaped+"; sleep 2")
	if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a launchd job ran outside the sandbox (%v): %s", err, out)
	}
	if out, err := b.sh(t, p, "/usr/bin/open "+b.main); err == nil {
		t.Errorf("Launch Services opened something for a confined process: %s", out)
	}
}

// TestAConfinedProcessSignalsOnlyItsOwn_REQ_SBX_001: a confined process cannot signal a
// process outside its sandbox — the daemon, the user's shells — and can signal its own
// children, which a shell does at every interrupted command.
func TestAConfinedProcessSignalsOnlyItsOwn_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	victim := exec.CommandContext(t.Context(), "/bin/sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = victim.Process.Kill(); _ = victim.Wait() })
	pid := strconv.Itoa(victim.Process.Pid)
	if out, err := b.sh(t, p, "/bin/kill -TERM "+pid); err == nil {
		t.Errorf("a confined process signalled a process outside its sandbox: %s", out)
	}
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("the process outside the sandbox is gone: %v", err)
	}
	if out, err := b.sh(t, p, "/bin/sleep 30 & /bin/kill -TERM $! && wait $!; echo ended"); err != nil || !strings.Contains(out, "ended") {
		t.Errorf("a confined process could not signal its own child: %v %s", err, out)
	}
}

// TestAConfinedProcessReachesNoSessionService_REQ_SBX_001: the preferences daemon writes a
// file in the user's home for whoever asks. A confined process's request never lands.
func TestAConfinedProcessReachesNoSessionService_REQ_SBX_001(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	domainName := "io.github.ecrespo.umbral.sandbox-test"
	t.Cleanup(func() { _ = exec.Command("/usr/bin/defaults", "delete", domainName).Run() })
	_, _ = b.sh(t, p, "/usr/bin/defaults write "+domainName+" planted yes")
	if out, err := exec.CommandContext(t.Context(), "/usr/bin/defaults", "read", domainName, "planted").CombinedOutput(); err == nil {
		t.Errorf("a preference written by a confined process is there: %s", out)
	}
	// What a program needs still answers: who the user is, and a name.
	for name, line := range map[string]string{"the user's name": "/usr/bin/id -un", "a shell": "/bin/zsh -c 'echo ok'"} {
		if out, err := b.sh(t, p, line); err != nil {
			t.Errorf("%s was refused: %v %s", name, err, out)
		}
	}
}

// TestAConfinedProcessCannotLinkADeniedFile_REQ_SBX_002: a hard link or a clone of a denied
// file into the worktree would be a readable copy of it. Both are refused, and so is the
// file under another spelling of its path on a file system that ignores case.
func TestAConfinedProcessCannotLinkADeniedFile_REQ_SBX_002(t *testing.T) {
	b := newBench(t)
	p := b.profile(t)
	db := filepath.Join(b.data, "umbral.db")
	for name, line := range map[string]string{
		"a hard link":                  "/bin/ln " + db + " " + filepath.Join(b.mine, "hard") + " && cat " + filepath.Join(b.mine, "hard"),
		"a clone":                      "/bin/cp -c " + db + " " + filepath.Join(b.mine, "clone") + " && cat " + filepath.Join(b.mine, "clone"),
		"a rename":                     "/bin/mv " + db + " " + filepath.Join(b.mine, "moved"),
		"another spelling of the path": "cat " + filepath.Join(filepath.Dir(b.data), "DATA", "umbral.db"),
	} {
		if out, err := b.sh(t, p, line); err == nil {
			t.Errorf("%s gave the database: %s", name, out)
		}
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("the database is gone: %v", err)
	}
}

// TestTheAdapterSaysWhatItEnforces_REQ_SBX_003: on a Mac that can confine, the status names
// the mechanism and no reason.
func TestTheAdapterSaysWhatItEnforces_REQ_SBX_003(t *testing.T) {
	b := newBench(t)
	if st := b.sb.Status(); st.State != domain.StateEnforcing || st.Mechanism != Mechanism || st.Reason != "" {
		t.Errorf("Status = %+v", st)
	}
}

// probeNet makes the test binary report what it can do on the network: its arguments are a
// port it may connect to, one it may not, one it may bind and one it may not.
const probeNet = "--probe-net"

// probeInbound makes the test binary listen on every interface on the port it is given, say
// so, and report whether a connection reached it.
const probeInbound = "--probe-inbound"

// TestMain makes the test binary its own network probe: run with probeNet it tries each thing
// from inside whatever sandbox it was started in, and prints `ok` or `refused` for each.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == probeInbound {
		ln, err := net.Listen("tcp4", "0.0.0.0:"+os.Args[2])
		if err != nil {
			fmt.Println("listen-refused")
			os.Exit(0)
		}
		fmt.Println("listening")
		_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(4 * time.Second))
		c, err := ln.Accept()
		if err != nil {
			fmt.Println("none")
			os.Exit(0)
		}
		_ = c.Close()
		fmt.Println("accepted")
		os.Exit(0)
	}
	if len(os.Args) == 6 && os.Args[1] == probeNet {
		said := func(err error) string {
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return "refused"
			}
			return "ok"
		}
		dial := func(network, port string) error {
			c, err := net.Dial(network, "127.0.0.1:"+port)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			if network == "udp4" {
				_, err = c.Write([]byte("x"))
			}
			return err
		}
		listen := func(port string) error {
			ln, err := net.Listen("tcp4", "127.0.0.1:"+port)
			if err == nil {
				_ = ln.Close()
			}
			return err
		}
		// The port it may bind, on every interface. The rules let this bind through, and
		// TestMacOSLeavesAConfinedListenerOpenToTheOutside_REQ_SBX_008 measures that a
		// connection from outside reaches it: REQ-SBX-008's macOS limit, accepted.
		everywhere := func(port string) error {
			ln, err := net.Listen("tcp4", "0.0.0.0:"+port)
			if err == nil {
				_ = ln.Close()
			}
			return err
		}
		// No bind at all: the kernel would choose the port, on every interface.
		unbound := func() error {
			fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
			if err != nil {
				return err
			}
			defer func() { _ = syscall.Close(fd) }()
			return syscall.Listen(fd, 1)
		}
		// A datagram to the very port the profile may connect to: the port is TCP's alone.
		fmt.Printf("connect-ok=%s connect-no=%s bind-ok=%s bind-no=%s bind-any=%s udp=%s udp-to-its-port=%s listen-everywhere=%s listen-unbound=%s\n",
			said(dial("tcp4", os.Args[2])), said(dial("tcp4", os.Args[3])),
			said(listen(os.Args[4])), said(listen(os.Args[5])), said(listen("0")),
			said(dial("udp4", os.Args[3])), said(dial("udp4", os.Args[2])),
			said(everywhere(os.Args[4])), said(unbound()))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestAConfinedProcessReachesOnlyItsPorts_REQ_SBX_008: with a network in its profile a process
// connects to the loopback's ports it may connect to and binds the ones it may bind — no
// other port, no port of the kernel's choosing, and no datagram. Without one the same probe
// reaches all of it.
func TestAConfinedProcessReachesOnlyItsPorts_REQ_SBX_008(t *testing.T) {
	b := newBench(t)
	if !b.sb.Status().Network {
		t.Fatalf("an enforcing sandbox on macOS does not confine the network: %+v", b.sb.Status())
	}
	listener := func() string {
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	}
	free := func() string {
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	}
	port := func(s string) uint16 {
		n, err := strconv.ParseUint(s, 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		return uint16(n)
	}
	allowed, other, bindOK, bindNo := listener(), listener(), free(), free()
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
		t.Logf("%s", out)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return lines[len(lines)-1]
	}
	p := b.profile(t)
	p.Net = &domain.Net{Connect: []uint16{port(allowed)}, Bind: []uint16{port(bindOK)}}
	if got, want := run(p), "connect-ok=ok connect-no=refused bind-ok=ok bind-no=refused bind-any=refused udp=refused udp-to-its-port=refused listen-everywhere=ok listen-unbound=refused"; got != want {
		t.Errorf("with a network profile the probe says %q, want %q", got, want)
	}
	if got, want := run(b.profile(t)), "connect-ok=ok connect-no=ok bind-ok=ok bind-no=ok bind-any=ok udp=ok udp-to-its-port=ok listen-everywhere=ok listen-unbound=ok"; got != want {
		t.Errorf("without a network profile the probe says %q, want %q: the test proves nothing", got, want)
	}
}

// outward is an IPv4 address of this machine's that is not the loopback's, or "".
func outward(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				return n.IP.String()
			}
		}
	}
	return ""
}

// TestMacOSLeavesAConfinedListenerOpenToTheOutside_REQ_SBX_008 measures REQ-SBX-008's macOS
// limit: the rules name the loopback, but a `local` filter's host is not checked and inbound
// is checked at `listen`, not at each connection, so a listener on every interface on a port
// the profile may bind is reached from the machine's outward address. The loopback's
// connection proves the probe. Should this fail, macOS refuses that connection now: the limit
// is gone, and REQ-SBX-008 and this test say so.
func TestMacOSLeavesAConfinedListenerOpenToTheOutside_REQ_SBX_008(t *testing.T) {
	b := newBench(t)
	addr := outward(t)
	if addr == "" {
		t.Skip("this machine has no outward IPv4 address to connect from")
	}
	reached := func(host string) string {
		t.Helper()
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		p := b.profile(t)
		p.Net = &domain.Net{Bind: []uint16{uint16(port)}}
		argv, err := b.sb.Command(p, []string{os.Args[0], probeInbound, strconv.Itoa(port)})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Wait() }()
		lines := bufio.NewScanner(out)
		if !lines.Scan() {
			t.Fatal("the probe said nothing")
		}
		if first := lines.Text(); first != "listening" {
			return first
		}
		d := net.Dialer{Timeout: 2 * time.Second}
		if c, err := d.DialContext(t.Context(), "tcp4", net.JoinHostPort(host, strconv.Itoa(port))); err == nil {
			_ = c.Close()
		} else {
			t.Logf("dial %s: %v", host, err)
		}
		if !lines.Scan() {
			t.Fatal("the probe said nothing after listening")
		}
		return lines.Text()
	}
	if got := reached("127.0.0.1"); got != "accepted" {
		t.Fatalf("a connection over the loopback: the probe says %q, want accepted: the test proves nothing", got)
	}
	if got := reached(addr); got != "accepted" {
		t.Errorf("a connection to %s, the machine's outward address: the probe says %q; the limit REQ-SBX-008 records is gone", addr, got)
	}
}
