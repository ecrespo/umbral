package shellinteg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// osc matches one OSC sequence terminated by BEL or ST, which is how the daemon's parser
// will see them (API Spec §7).
var osc = regexp.MustCompile(`\x1b\]([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// shellUnderTest is one shell the bootstrap must support.
type shellUnderTest struct {
	kind Kind
	bin  string
	// extraArgs keeps the shell interactive and quiet under a PTY.
	extraArgs []string
}

var shellsUnderTest = []shellUnderTest{
	{kind: Bash, bin: "bash", extraArgs: []string{"-i"}},
	{kind: Zsh, bin: "zsh", extraArgs: []string{"-i"}},
	{kind: Fish, bin: "fish", extraArgs: []string{"-i"}},
}

// TestBootstrapEmitsOSC133_REQ_BLK_005 runs each real shell under a PTY with the
// bootstrap injected, sends one command, and checks that the sequences the block state
// machine depends on actually arrive.
//
// It uses a PTY rather than a pipe because every one of these hooks only runs in an
// interactive shell, and a shell on a pipe is not interactive. A test on a pipe would
// pass against a bootstrap that emits nothing.
func TestBootstrapEmitsOSC133_REQ_BLK_005(t *testing.T) {
	t.Parallel()

	for _, sh := range shellsUnderTest {
		t.Run(string(sh.kind), func(t *testing.T) {
			t.Parallel()

			bin, err := exec.LookPath(sh.bin)
			if err != nil {
				t.Skipf("%s is not installed: %v", sh.bin, err)
			}

			output := runShell(t, bin, sh.extraArgs, "printf hi\n")
			sequences := oscSequences(output)

			// REQ-BLK-001: the daemon opens a block on 133;C, with the command line from
			// 633;E and the directory from OSC 7.
			assertSequence(t, sequences, "133;C", "OSC 133;C, which is what opens a block")
			assertPrefix(t, sequences, "633;E;", "OSC 633;E, which carries the command line")
			assertPrefix(t, sequences, "7;file://", "OSC 7, which carries the working directory")

			// REQ-BLK-002: the block closes on 133;D with the exit code.
			assertPrefix(t, sequences, "133;D;", "OSC 133;D, which closes the block")

			// Prompt markers, used for block navigation in the TUI.
			assertSequence(t, sequences, "133;A", "OSC 133;A, the prompt start marker")
			assertSequence(t, sequences, "133;B", "OSC 133;B, the prompt end marker")

			// The command line must round-trip, or the block records the wrong command.
			if got := find(sequences, "633;E;"); got != "633;E;printf hi" {
				t.Errorf("command line sequence = %q, want %q", got, "633;E;printf hi")
			}

			// The command really ran: a bootstrap that emits markers but breaks the shell
			// would pass every check above.
			if !strings.Contains(output, "hi") {
				t.Errorf("the command produced no output; the bootstrap may have broken the shell\n%s",
					visible(output))
			}
		})
	}
}

// TestBootstrapPreservesTheUserConfiguration is the property that keeps this from being a
// terminal people refuse to use: injecting the integration must not drop the user's own
// rc file.
func TestBootstrapPreservesTheUserConfiguration(t *testing.T) {
	t.Parallel()

	for _, sh := range shellsUnderTest {
		t.Run(string(sh.kind), func(t *testing.T) {
			t.Parallel()

			bin, err := exec.LookPath(sh.bin)
			if err != nil {
				t.Skipf("%s is not installed: %v", sh.bin, err)
			}
			if sh.kind == Fish {
				t.Skip("fish's --init-command runs after config.fish, so there is nothing to restore")
			}

			home := t.TempDir()
			rcName := map[Kind]string{Bash: ".bashrc", Zsh: ".zshrc"}[sh.kind]
			marker := "umbral-user-rc-was-loaded"
			rc := fmt.Sprintf("export UMBRAL_TEST_MARKER=%s\n", marker)
			if err := os.WriteFile(filepath.Join(home, rcName), []byte(rc), 0o600); err != nil {
				t.Fatalf("write the fake %s: %v", rcName, err)
			}

			output := runShellInHome(t, bin, sh.extraArgs, home, "echo $UMBRAL_TEST_MARKER\n")
			if !strings.Contains(output, marker) {
				t.Errorf("the user's %s was not loaded; the marker never appeared\n%s",
					rcName, visible(output))
			}
		})
	}
}

// TestBootstrapSurvivesAPromptThatRewritesItself is the regression for the clause in
// T-F0-08 about Starship and powerlevel10k. Those frameworks reassign the prompt variable
// on every prompt, so markers applied once are gone after the first command.
//
// The fake rc below reproduces that behaviour without needing either framework installed,
// which matters because a CI runner has neither and the developer machine that found this
// bug had Starship in its own rc. The assertion is on the *second* command: a one-time
// wrap passes the first.
func TestBootstrapSurvivesAPromptThatRewritesItself(t *testing.T) {
	t.Parallel()

	fakeRC := map[Kind]string{
		Bash: "PROMPT_COMMAND='PS1=\"fake> \"'\n",
		Zsh: "autoload -Uz add-zsh-hook\n" +
			"__fake_precmd() { PROMPT='fake> ' }\n" +
			"add-zsh-hook precmd __fake_precmd\n",
	}
	rcName := map[Kind]string{Bash: ".bashrc", Zsh: ".zshrc"}

	for _, sh := range shellsUnderTest {
		t.Run(string(sh.kind), func(t *testing.T) {
			t.Parallel()

			bin, err := exec.LookPath(sh.bin)
			if err != nil {
				t.Skipf("%s is not installed: %v", sh.bin, err)
			}
			rc, ok := fakeRC[sh.kind]
			if !ok {
				t.Skip("fish has no prompt variable to rewrite; its prompt is a function, wrapped once")
			}

			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, rcName[sh.kind]), []byte(rc), 0o600); err != nil {
				t.Fatalf("write the fake rc: %v", err)
			}

			output := runShellInHome(t, bin, sh.extraArgs, home, "printf one\nprintf two\n")
			sequences := oscSequences(output)

			// Two commands, so two blocks, and the prompt markers must survive both.
			if got := count(sequences, "133;C"); got < 2 {
				t.Errorf("got %d command-start markers for 2 commands: %q", got, sequences)
			}
			if got := count(sequences, "133;B"); got < 2 {
				t.Errorf("got %d prompt-end markers after a prompt that rewrites itself;"+
					" the markers were applied once and then lost: %q", got, sequences)
			}
			if !strings.Contains(output, "fake>") {
				t.Errorf("the fake prompt never rendered, so this test proved nothing\n%s", visible(output))
			}
		})
	}
}

func TestDetectKind(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path string
		want Kind
		err  bool
	}{
		{path: "/bin/bash", want: Bash},
		{path: "/usr/local/bin/bash-5.2", want: Bash},
		{path: "/usr/bin/zsh", want: Zsh},
		{path: "/opt/homebrew/bin/fish", want: Fish},
		{path: "/bin/sh", err: true},
		{path: "/usr/bin/pwsh", err: true},
	} {
		got, err := DetectKind(tc.path)
		if tc.err {
			if !errors.Is(err, ErrUnsupportedShell) {
				t.Errorf("DetectKind(%q) = %q, %v; want ErrUnsupportedShell", tc.path, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("DetectKind(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
}

func TestPrepareCleansUpAfterItself(t *testing.T) {
	t.Parallel()

	b, err := Prepare("/bin/bash")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(b.Args) != 2 || b.Args[0] != "--init-file" {
		t.Errorf("bash args = %v, want --init-file <path>", b.Args)
	}
	script := b.Args[1]
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the bootstrap script was not written: %v", err)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Errorf("the bootstrap script survived Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestPrepareZshUsesATemporaryZDOTDIR(t *testing.T) {
	t.Parallel()

	b, err := Prepare("/usr/bin/zsh")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() { _ = b.Close() }()

	var zdotdir string
	for _, kv := range b.Env {
		if after, ok := strings.CutPrefix(kv, "ZDOTDIR="); ok {
			zdotdir = after
		}
	}
	if zdotdir == "" {
		t.Fatalf("zsh bootstrap set no ZDOTDIR: %v", b.Env)
	}
	if _, err := os.Stat(filepath.Join(zdotdir, ".zshrc")); err != nil {
		t.Errorf("no .zshrc in the temporary ZDOTDIR: %v", err)
	}
	if len(b.Args) != 0 {
		t.Errorf("zsh needs no extra arguments, got %v", b.Args)
	}
}

// runShell starts the shell under a PTY with the bootstrap injected, feeds it the given
// input followed by exit, and returns everything it wrote.
func runShell(t *testing.T, bin string, extraArgs []string, input string) string {
	t.Helper()
	return runShellInHome(t, bin, extraArgs, "", input)
}

func runShellInHome(t *testing.T, bin string, extraArgs []string, home, input string) string {
	t.Helper()

	b, err := Prepare(bin)
	if err != nil {
		t.Fatalf("Prepare(%s): %v", bin, err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("Bootstrap.Close: %v", err)
		}
	})

	// The context bounds the shell even if the kill below is never reached.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, b.Argv(extraArgs...)...)
	cmd.Env = append(os.Environ(), b.Env...)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "UMBRAL_TEST=1")
	if home != "" {
		cmd.Env = append(cmd.Env, "HOME="+home)
		// bash reads ~/.bashrc through the bootstrap's fallback, which resolves HOME at
		// script time; the explicit hand-off is what the daemon does too.
		cmd.Env = append(cmd.Env, "UMBRAL_BASH_RC="+filepath.Join(home, ".bashrc"))
	}
	cmd.Dir = t.TempDir()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("start %s under a pty: %v", bin, err)
	}
	defer func() { _ = ptmx.Close() }()

	var (
		mu  sync.Mutex
		out strings.Builder
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	// Let the shell reach its first prompt before typing, or the input is swallowed by
	// the line editor while it is still starting up.
	time.Sleep(750 * time.Millisecond)
	if _, err := io.WriteString(ptmx, input); err != nil {
		t.Fatalf("write to the pty: %v", err)
	}
	time.Sleep(750 * time.Millisecond)
	if _, err := io.WriteString(ptmx, "exit\n"); err != nil && !errors.Is(err, io.EOF) {
		t.Logf("write exit: %v", err)
	}

	// Generous on purpose. This bound exists to stop a wedged shell hanging the suite, not
	// to measure anything: three real shells fork in parallel here, and under
	// `go test -race ./...` on a loaded machine bash, zsh and fish have taken past fifteen
	// seconds between them — a red build that says nothing about the code. A timeout that
	// fires on load is a worse signal than no timeout at all, because it is read as a
	// finding.
	const exitBudget = 60 * time.Second
	select {
	case <-done:
	case <-time.After(exitBudget):
		_ = cmd.Process.Kill()
		t.Fatalf("the shell did not exit within %v", exitBudget)
	}
	_ = cmd.Wait()

	mu.Lock()
	defer mu.Unlock()
	return out.String()
}

// oscSequences extracts the payload of every OSC sequence in the stream.
func oscSequences(output string) []string {
	matches := osc.FindAllStringSubmatch(output, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func assertSequence(t *testing.T, sequences []string, want, why string) {
	t.Helper()

	for _, s := range sequences {
		if s == want {
			return
		}
	}
	t.Errorf("missing %s\ngot sequences: %q", why, sequences)
}

func assertPrefix(t *testing.T, sequences []string, prefix, why string) {
	t.Helper()

	if find(sequences, prefix) == "" {
		t.Errorf("missing %s\ngot sequences: %q", why, sequences)
	}
}

func find(sequences []string, prefix string) string {
	for _, s := range sequences {
		if strings.HasPrefix(s, prefix) {
			return s
		}
	}
	return ""
}

// visible renders control characters so a failure message is readable.
func visible(s string) string {
	return strings.NewReplacer("\x1b", "<ESC>", "\x07", "<BEL>", "\r", "<CR>").Replace(s)
}

// count reports how many sequences exactly equal want.
func count(sequences []string, want string) int {
	n := 0
	for _, s := range sequences {
		if s == want {
			n++
		}
	}
	return n
}
