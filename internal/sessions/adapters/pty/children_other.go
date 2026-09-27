//go:build !linux && !darwin

package pty

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// childrenOf lists the processes whose parent is pid, through pgrep.
func childrenOf(pid int) []int {
	// Bounded: the cancel it serves has 500 ms in all.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	outb, err := exec.CommandContext(ctx, "pgrep", "-P", strconv.Itoa(pid)).Output() //nolint:gosec // a fixed program and a pid, nothing a caller can shape
	if err != nil {
		return nil
	}
	var out []int
	for _, f := range strings.Fields(string(outb)) {
		if child, err := strconv.Atoi(f); err == nil {
			out = append(out, child)
		}
	}
	return out
}
