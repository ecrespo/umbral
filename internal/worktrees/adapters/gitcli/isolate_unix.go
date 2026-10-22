//go:build unix

package gitcli

import (
	"os/exec"
	"syscall"
	"time"
)

// isolate runs git in a session of its own, with no controlling terminal, so nothing it starts
// — a hook, a pinentry — can open /dev/tty and wait on a person. A timeout kills the whole
// process group, not only git, and Wait gives up on pipes a stray child still holds.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
}
