//go:build linux

package landlock_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	os.Exit(m.Run())
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
