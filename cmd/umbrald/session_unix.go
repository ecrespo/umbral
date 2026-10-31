//go:build unix

package main

import "golang.org/x/sys/unix"

// sessionOf is the POSIX session a pid belongs to, which the agent's-terminal check reads: an
// agent's shell and a hosted agent each lead one, so what their children left behind is found by
// it (REQ-ORC-004).
func sessionOf(...treeHolder) func(pid int) (int, error) { return unix.Getsid }

// treeHolder names the agent whose process tree holds a pid; Unix asks the session instead.
type treeHolder interface{ AgentTreeHolding(pid int) int }
