//go:build unix

package main

import "golang.org/x/sys/unix"

// sessionOf is the POSIX session pid belongs to, which the agent's-terminal check reads.
func sessionOf(pid int) (int, error) { return unix.Getsid(pid) }
