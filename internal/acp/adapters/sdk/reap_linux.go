package sdk

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// forgetAtExit: on Linux the agent's pid is forgotten when it exits, once endSession has seen
// nothing left in its session.
const forgetAtExit = true

// sessionDeadline bounds endSession: a session still not empty by then keeps the pid listed.
const sessionDeadline = 2 * time.Second

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
// other process groups of it too — and reports whether the session was then seen empty, its
// zombies aside (REQ-ORC-004). A process can fork while it is killed, so it scans until a pass
// finds none or sessionDeadline passes. Each process is signalled through a pidfd, after its
// session is read again behind the pidfd, so a pid reused since the scan is never signalled.
// One that left the session with setsid is not found, the residual Tech §5.3 records.
func endSession(sid int) bool {
	_ = unix.Kill(-sid, unix.SIGKILL)
	deadline := time.Now().Add(sessionDeadline)
	for {
		live := false
		for _, pid := range sessionMembers(sid) {
			if pid == sid {
				continue
			}
			live = true
			killInSession(pid, sid)
		}
		if !live {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// killInSession sends SIGKILL to pid only if, once a pidfd holds it, it is still in sid. Where
// no pidfd can be had (an old kernel, a full descriptor table) the session is read again and
// the pid signalled directly: the agent is an unreaped zombie, so sid is not reused, and a
// process still in it is the agent's. Either way a session not emptied keeps the pid listed.
func killInSession(pid, sid int) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err == unix.ESRCH {
		return
	}
	if err != nil {
		if s, live, ok := procSession(pid); ok && live && s == sid {
			_ = unix.Kill(pid, unix.SIGKILL)
		}
		return
	}
	defer func() { _ = unix.Close(fd) }()
	if s, live, ok := procSession(pid); ok && live && s == sid {
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	}
}

// sessionMembers lists the live (not zombie) processes whose session id is sid.
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
		if s, live, ok := procSession(pid); ok && live && s == sid {
			out = append(out, pid)
		}
	}
	return out
}

// procSession reads a process's session id from /proc/<pid>/stat, and whether it is not a
// zombie: after the command's closing parenthesis come state, ppid, pgrp and session.
func procSession(pid int) (sid int, live, ok bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false, false
	}
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false, false
	}
	fields := bytes.Fields(stat[i+1:])
	if len(fields) < 4 {
		return 0, false, false
	}
	s, err := strconv.Atoi(string(fields[3]))
	if err != nil {
		return 0, false, false
	}
	return s, string(fields[0]) != "Z", true
}
