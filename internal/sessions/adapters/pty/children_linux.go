package pty

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// childrenOf lists the processes whose parent is pid, from /proc.
func childrenOf(pid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		child, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command name is in parentheses and may hold spaces; the fields after the
		// last ')' are state, then the parent's pid.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) > 1 && fields[1] == strconv.Itoa(pid) {
			out = append(out, child)
		}
	}
	return out
}
