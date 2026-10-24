package main

// agentPeer is api.Config.AgentPeer: a client runs inside an agent's terminal when one of the
// agents — the agent runtime's thread shells and the hosted ACP agents' processes — is the
// client's process or one of its parents, or leads the POSIX session the client is in
// (REQ-ORC-004, deltas `2026-10-full-auto` and `2026-10-hosted-agent-peer`). The session catches
// a process the shell's children left to init; a process that leaves the session with setsid
// and then forks away from its parents escapes both, which the delta records.
//
// While any agent lives, a peer that can no longer be read — it exited after connecting,
// leaving the socket to a child — is taken for an agent's: the methods it is refused are the
// user's, and the user's own clients stay alive while they call them.
func agentPeer(agents func() []int, ancestors func(pid int) []int, sid func(pid int) (int, error)) func(pid int) bool {
	return func(pid int) bool {
		known := map[int]bool{}
		for _, p := range agents() {
			known[p] = true
		}
		if len(known) == 0 {
			return false
		}
		chain := ancestors(pid)
		for _, p := range chain {
			if known[p] {
				return true
			}
		}
		s, err := sid(pid)
		if err != nil || len(chain) == 0 {
			return true
		}
		return known[s]
	}
}
