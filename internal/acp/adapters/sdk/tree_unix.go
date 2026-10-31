//go:build unix

package sdk

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateTree starts the agent in its own session, and so its own process group: the shells
// and servers it starts end with it, and a process it leaves behind is still known by its
// session for REQ-ORC-004.
func isolateTree(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

// killGroup kills the agent's process group.
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// openNonblocking opens p for reading without waiting on a FIFO for a writer.
func openNonblocking(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // a git file in the thread's own directory tree
}
