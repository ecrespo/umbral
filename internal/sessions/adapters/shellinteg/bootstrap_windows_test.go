package shellinteg

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	conpty "github.com/aymanbagabas/go-pty"
)

// pwshOSC matches one OSC sequence terminated by BEL or ST.
var pwshOSC = regexp.MustCompile(`\x1b\]([^\x07\x1b]*)(?:\x07|\x1b\\)`)

// pwshConsole is a pwsh session under ConPTY with the bootstrap injected, and what it printed.
type pwshConsole struct {
	p   conpty.Pty
	mu  sync.Mutex
	out bytes.Buffer
}

func startPwsh(t *testing.T, parent string) *pwshConsole {
	t.Helper()
	bin, err := exec.LookPath("pwsh")
	if err != nil {
		// REQ-WIN-002 is proved on CI's windows job; a runner without pwsh must not pass it by skipping.
		if os.Getenv("CI") != "" {
			t.Fatalf("PowerShell 7 is not installed on CI: %v", err)
		}
		t.Skipf("PowerShell 7 is not installed: %v", err)
	}
	b, err := Prepare(bin, parent)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	// go-pty directly: the session adapter is another adapter, which this one may not import.
	p, err := conpty.New()
	if err != nil {
		t.Fatalf("create a pseudo console: %v", err)
	}
	if err := p.Resize(160, 40); err != nil {
		t.Fatal(err)
	}
	cmd := p.Command(bin, b.Argv()...)
	cmd.Env = append(os.Environ(), b.Env...)
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		t.Fatalf("start pwsh under ConPTY: %v", err)
	}
	c := &pwshConsole{p: p}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = p.Close()
	})
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := p.Read(buf)
			c.mu.Lock()
			c.out.Write(buf[:n])
			c.mu.Unlock()
			if err != nil || n == 0 {
				return
			}
		}
	}()
	return c
}

// sequences are the OSC payloads printed so far.
func (c *pwshConsole) sequences() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, m := range pwshOSC.FindAllStringSubmatch(c.out.String(), -1) {
		out = append(out, m[1])
	}
	return out
}

// await waits until the n-th sequence equal to want has been printed.
func (c *pwshConsole) await(t *testing.T, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		for _, s := range c.sequences() {
			if s == want {
				count++
			}
		}
		if count >= n {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("pwsh never printed OSC %q (#%d); it printed %q", want, n, c.out.String())
}

func (c *pwshConsole) typeLine(t *testing.T, line string) {
	t.Helper()
	if _, err := c.p.Write([]byte(line + "\r")); err != nil {
		t.Fatalf("type %q: %v", line, err)
	}
}

// TestPwshBootstrapEmitsOSC133_REQ_WIN_002 is REQ-BLK-008 on Windows: PowerShell 7 under ConPTY
// announces its prompts, each command line, the command's start and its end with the exit code —
// a native command's own code, 1 for a failed cmdlet — and its working directory.
func TestPwshBootstrapEmitsOSC133_REQ_WIN_002(t *testing.T) {
	t.Parallel()

	// A space and a quote in the runtime directory, as REQ-TERM-012 found for fish.
	parent := filepath.Join(t.TempDir(), "Application Support", "it's Umbral")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	c := startPwsh(t, parent)
	c.await(t, "133;B", 1)

	c.typeLine(t, "Write-Output ('h'+'i')")
	c.await(t, "133;D;0", 1)
	c.typeLine(t, "cmd /c exit 7")
	c.await(t, "133;D;7", 1)
	c.typeLine(t, "Get-Item no-such-file-here")
	c.await(t, "133;D;1", 1)

	seqs := c.sequences()
	for _, want := range []string{"133;A", "133;B", "133;C", "633;E;Write-Output ('h'+'i')", "633;E;cmd /c exit 7"} {
		found := false
		for _, s := range seqs {
			found = found || s == want
		}
		if !found {
			t.Errorf("no OSC %q among %q", want, seqs)
		}
	}
	cwd := false
	for _, s := range seqs {
		cwd = cwd || strings.HasPrefix(s, "7;file://")
	}
	if !cwd {
		t.Errorf("no OSC 7 among %q", seqs)
	}
	c.mu.Lock()
	ran := strings.Contains(c.out.String(), "hi")
	c.mu.Unlock()
	if !ran {
		t.Error("the command produced no output; the bootstrap may have broken the shell")
	}
}

// TestAnEmptyLineOpensNoBlock_REQ_WIN_002: Enter on an empty prompt starts nothing, as in the
// other shells, so no block is recorded for it.
func TestAnEmptyLineOpensNoBlock_REQ_WIN_002(t *testing.T) {
	t.Parallel()

	c := startPwsh(t, t.TempDir())
	c.await(t, "133;B", 1)
	c.typeLine(t, "")
	c.await(t, "133;B", 2)
	c.typeLine(t, "Write-Output done")
	c.await(t, "133;D;0", 1)
	starts := 0
	for _, s := range c.sequences() {
		if s == "133;C" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("%d blocks opened for one command and one empty line", starts)
	}
}
