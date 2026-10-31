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

// tree is the agent's process group.
type tree struct{ pid int }

// joinTree is the group the agent leads since isolateTree.
func joinTree(pid int) (*tree, error) { return &tree{pid: pid}, nil }

// endTreeAtExit is false: what the agent leaves in its session is the reaper's (reap_*.go).
const endTreeAtExit = false

// signal kills the agent's process group, while its pid is still the agent's.
func (t *tree) signal() { _ = syscall.Kill(-t.pid, syscall.SIGKILL) }

// end does nothing more: a process group has no handle to close.
func (t *tree) end() {}

// holds is false: the POSIX session check finds what the agent left (REQ-ORC-004).
func (t *tree) holds(int) bool { return false }

// openNonblocking opens p for reading without waiting on a FIFO for a writer.
func openNonblocking(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // a git file in the thread's own directory tree
}
