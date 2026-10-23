package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ecrespo/umbral/internal/integrations/adapters/proctree"
)

// TestAClientInsideAnAgentsTerminalIsKnown_REQ_ORC_004: a process is inside an agent's terminal
// when a thread shell is among its parents, or leads its session even after the process was
// left to init; a process under neither is not.
func TestAClientInsideAnAgentsTerminalIsKnown_REQ_ORC_004(t *testing.T) {
	// A session leader standing in for a thread's shell, with a child that is in its session.
	shell := exec.CommandContext(t.Context(), "sh", "-c", `sleep 30 & echo $!; wait`)
	shell.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := shell.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-shell.Process.Pid, syscall.SIGKILL)
		_ = shell.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}

	shells := func() []int { return []int{shell.Process.Pid} }
	noSession := func(int) (int, error) { return 0, errors.New("no session") }
	if !agentPeer(shells, proctree.Ancestors, noSession)(child) {
		t.Error("a child of the thread's shell is not inside its terminal")
	}
	orphaned := func(pid int) []int { return []int{pid, 1} } // as if left to init
	if !agentPeer(shells, orphaned, unix.Getsid)(child) {
		t.Error("a process left to init in the shell's session is not inside its terminal")
	}
	if agentPeer(shells, proctree.Ancestors, unix.Getsid)(os.Getpid()) {
		t.Error("the test itself, outside the shell, is inside its terminal")
	}
	// A peer that is gone — it connected, then exited and left the socket to a child — cannot
	// be cleared, so it is taken for an agent's while any agent shell lives.
	gone := exec.CommandContext(t.Context(), "true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if !agentPeer(shells, proctree.Ancestors, unix.Getsid)(gone.Process.Pid) {
		t.Error("a peer that exited is let through as the user's")
	}
	if agentPeer(func() []int { return nil }, proctree.Ancestors, unix.Getsid)(child) {
		t.Error("with no agent shells, a process is inside one")
	}
}
