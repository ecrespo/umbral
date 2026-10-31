package pty

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// console reads everything a pseudo console prints, for a test to wait on.
type console struct {
	mu  sync.Mutex
	out bytes.Buffer
}

func readAll(t *testing.T, p ports.PTY) *console {
	t.Helper()
	c := &console{}
	go func() {
		buf := make([]byte, 4096)
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

func (c *console) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.out.String()
		c.mu.Unlock()
		if strings.Contains(got, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("the console never printed %q; it printed %q", want, c.out.String())
}

func cmdExe(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
}

// TestConPTYRunsAShellAndReportsItsExit_REQ_WIN_001 is a session's life on Windows: a program
// started on a pseudo console of the asked size, its output read back, its exit code reported.
func TestConPTYRunsAShellAndReportsItsExit_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	p, err := Open(ports.PTYSpec{
		Path: cmdExe(t),
		Args: []string{"/d", "/c", "echo umbral-conpty& exit 3"},
		Env:  os.Environ(),
		Dir:  t.TempDir(),
		Size: domain.Size{Cols: 100, Rows: 30},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = p.Close() }()
	out := readAll(t, p)
	out.waitFor(t, "umbral-conpty")

	code, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if err := p.Resize(domain.Size{Cols: 120, Rows: 40}); err != nil {
		t.Errorf("Resize after the exit: %v", err)
	}
}

// TestConPTYKillEndsTheShellAndWhatItRuns_REQ_WIN_001: a kill reaches the shell's whole job, so a
// command the shell started does not outlive it, as a process group's do not on Unix.
func TestConPTYKillEndsTheShellAndWhatItRuns_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	p, err := Open(ports.PTYSpec{
		Path: cmdExe(t),
		Args: []string{"/d", "/k"},
		Env:  os.Environ(),
		Dir:  t.TempDir(),
		Size: domain.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = p.Close() }()
	out := readAll(t, p)
	if _, err := p.Write([]byte("echo ready& ping -n 120 127.0.0.1\r")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "Pinging")

	// The shell's job holds what it runs — what the agent's-terminal check asks (REQ-ORC-004).
	tree, ok := p.(interface{ Holds(pid int) bool })
	if !ok {
		t.Fatal("a Windows session does not say what its job holds")
	}
	if tree.Holds(os.Getpid()) {
		t.Error("the shell's job holds the daemon")
	}
	pings := descendants(t, uint32(p.Pid()))
	if len(pings) == 0 {
		t.Fatal("the shell started nothing")
	}
	for _, pid := range pings {
		if !tree.Holds(pid) {
			t.Errorf("the shell's job does not hold its child %d", pid)
		}
	}

	if err := p.Signal(ports.SignalKill); err != nil {
		t.Fatalf("Signal(kill): %v", err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell outlived its kill")
	}
}

// TestConPTYInterruptStopsTheForegroundAndKeepsTheShell_REQ_WIN_001 is the agent's cancel on
// Windows (REQ-AGT-007): Ctrl-C ends what the shell runs, and the shell takes the next line.
func TestConPTYInterruptStopsTheForegroundAndKeepsTheShell_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	p, err := Open(ports.PTYSpec{
		Path: cmdExe(t),
		Args: []string{"/d", "/k"},
		Env:  os.Environ(),
		Dir:  t.TempDir(),
		Size: domain.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		_ = p.Signal(ports.SignalKill)
		_ = p.Close()
	}()
	out := readAll(t, p)
	if _, err := p.Write([]byte("ping -n 120 127.0.0.1\r")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "Pinging")
	if err := p.Signal(ports.SignalTermForeground); err != nil {
		t.Fatalf("Signal(term foreground): %v", err)
	}
	if _, err := p.Write([]byte("echo still-%OS%\r")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "still-Windows_NT")
}

// TestAProgramWithoutARunnableExtensionIsRefused: Windows has no executable bit, so a shell is
// what PATHEXT says Windows runs.
func TestAProgramWithoutARunnableExtensionIsRefused(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ports.PTYSpec{Path: path, Size: domain.Size{Cols: 80, Rows: 24}}); err == nil {
		t.Fatal("a text file was started as a shell")
	}
}

// descendants lists the processes below pid.
func descendants(t *testing.T, pid uint32) []int {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	children := map[uint32][]uint32{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID != pe.ParentProcessID {
			children[pe.ParentProcessID] = append(children[pe.ParentProcessID], pe.ProcessID)
		}
	}
	var out []int
	queue := append([]uint32(nil), children[pid]...)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		out = append(out, int(p))
		queue = append(queue, children[p]...)
	}
	return out
}
