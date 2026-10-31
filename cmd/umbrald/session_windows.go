package main

// treeHolder names the agent whose process tree — its Job Object — holds a pid, or 0.
type treeHolder interface{ AgentTreeHolding(pid int) int }

// sessionOf stands in for the POSIX session Windows does not have: the agent whose Job Object
// holds pid, which is every agent shell's and every hosted agent's on Windows, so a process
// their children left behind is still the agent's (REQ-ORC-004; delta `2026-10-windows-port`,
// decisions 4 and 10). A pid held by no agent answers 0, which no agent has.
func sessionOf(holders ...treeHolder) func(pid int) (int, error) {
	return func(pid int) (int, error) {
		for _, h := range holders {
			if agent := h.AgentTreeHolding(pid); agent > 0 {
				return agent, nil
			}
		}
		return 0, nil
	}
}
