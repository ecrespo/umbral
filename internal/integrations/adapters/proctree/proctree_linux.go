package proctree

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

func processTable() []entry {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []entry
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + d.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command name is in parentheses and may hold spaces; after the last ')' come the
		// state, then the parent's pid.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		out = append(out, entry{pid: pid, ppid: ppid})
	}
	return out
}

func argvOf(pid int) []string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(raw) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
}
