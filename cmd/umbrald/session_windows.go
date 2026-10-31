package main

// sessionOf answers 0: Windows has no POSIX session, so the agent's-terminal check walks a client's
// parents and nothing else (delta `2026-10-windows-port`, decision 4). 0 is no agent's pid.
func sessionOf(int) (int, error) { return 0, nil }
