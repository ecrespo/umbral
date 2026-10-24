package sdk

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

// forgetAtExit: on Linux the agent's pid is forgotten when it exits, once endSession has left
// nothing in its session.
const forgetAtExit = true

// waitExit blocks until the agent has exited and leaves it unreaped (WNOWAIT), so its pid —
// its group's and its session's id too — stays its own until the caller's cmd.Wait.
func waitExit(cmd *exec.Cmd) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return
		}
	}
}

// endSession kills every process in the session the exited, unreaped agent led — those in
// other process groups of it too — so nothing it started is left once its pid is forgotten
// (REQ-ORC-004). A process can fork while it is killed, so it scans until a pass finds none;
// one that left the session with setsid is not found, the residual Tech §5.3 records.
func endSession(sid int) {
	_ = unix.Kill(-sid, unix.SIGKILL)
	for range 50 {
		found := false
		for _, pid := range sessionMembers(sid) {
			if pid != sid {
				_ = unix.Kill(pid, unix.SIGKILL)
				found = true
			}
		}
		if !found {
			return
		}
	}
}

// sessionMembers lists the processes whose session id is sid, read from /proc/<pid>/stat.
func sessionMembers(sid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// After the command's closing parenthesis: state, ppid, pgrp, session.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		fields := bytes.Fields(stat[i+1:])
		if len(fields) > 3 {
			if s, err := strconv.Atoi(string(fields[3])); err == nil && s == sid {
				out = append(out, pid)
			}
		}
	}
	return out
}
