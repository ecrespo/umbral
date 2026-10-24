package sdk

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

// waitExit blocks until the agent has exited and leaves it unreaped (WNOWAIT), so its pid and
// its group's id stay its own until the caller's cmd.Wait. It reports false: not reaped.
func waitExit(cmd *exec.Cmd) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return false
		}
	}
}
