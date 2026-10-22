// Package proctree lists the processes of a pane for agent detection (REQ-XAG-001). It reads
// the kernel's process table and each process's argv, and never runs a command.
package proctree

import (
	"sort"

	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/integrations/ports"
)

// The bounds of one listing: a pane's root, what it runs, and what that runs.
const (
	maxDepth     = 8
	maxProcesses = 256
)

// Tree implements ports.ProcessTree over the operating system's process table.
type Tree struct{}

// entry is one row of the process table.
type entry struct {
	pid, ppid int
}

// snapshot is the table's parent links, read once; argv is read only for the processes a
// subtree lists.
type snapshot struct {
	children map[int][]int
}

// Snapshot reads the process table.
func (Tree) Snapshot() ports.ProcessSnapshot {
	s := snapshot{children: map[int][]int{}}
	for _, e := range processTable() {
		s.children[e.ppid] = append(s.children[e.ppid], e.pid)
	}
	for _, kids := range s.children {
		sort.Ints(kids)
	}
	return s
}

// Subtree lists pid, then up to maxDepth levels under it, breadth first and in pid order within
// a level. A process that exits while it is read is left out.
func (s snapshot) Subtree(pid int) []domain.Process {
	var out []domain.Process
	level := []int{pid}
	for depth := 0; depth <= maxDepth && len(level) > 0; depth++ {
		for _, p := range level {
			if len(out) == maxProcesses {
				return out
			}
			if argv := argvOf(p); len(argv) > 0 {
				out = append(out, domain.Process{PID: p, Argv: argv})
			}
		}
		var next []int
		for _, p := range level {
			next = append(next, s.children[p]...)
		}
		level = next
	}
	return out
}
