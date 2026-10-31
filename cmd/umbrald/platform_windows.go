package main

// What this platform serves of F2d (delta `2026-10-windows-port`, decision 11): the live
// handoff passes descriptors with SCM_RIGHTS, and Windows OpenSSH's stream-local forwarding is
// not reliable enough for REQ-RMT-001, so neither is offered.
const (
	liveHandoffSupported = false
	remoteHostsSupported = false
)
