//go:build !linux

package sdk

import "os/exec"

// waitExit reaps the agent with cmd.Wait and reports true. Without waitid's WNOWAIT (macOS)
// nothing is known of the exit before the pid is free, so the group is not signalled then: a
// child the agent left behind runs on until the thread's agent is closed or the daemon stops.
func waitExit(cmd *exec.Cmd) bool {
	_ = cmd.Wait()
	return true
}
