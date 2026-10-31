//go:build !linux && !darwin && !windows

package api

import "net"

// peerPID cannot read a peer's pid on this platform: 0, which the agent check refuses.
func peerPID(net.Conn) int { return 0 }
