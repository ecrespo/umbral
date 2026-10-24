//go:build !linux

package sdk

import "os/exec"

// forgetAtExit: without waitid's WNOWAIT (macOS) the pid is free as soon as the agent is
// reaped, so what it left in its session cannot be found by its id then. The pid is kept
// listed until the thread's agent is closed instead, so a process the agent left behind is
// still refused the user's methods while it may run (REQ-ORC-004).
const forgetAtExit = false

// waitExit reaps the agent.
func waitExit(cmd *exec.Cmd) { _ = cmd.Wait() }

// endSession does nothing: the session's id may already be someone else's.
func endSession(int) {}
