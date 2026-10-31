package sessions

import (
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// treePTY is a PTY whose shell's processes sit in a tree of their own, as a Job Object holds
// them on Windows; only what the agent's-terminal check asks is implemented.
type treePTY struct {
	ports.PTY
	pid  int
	open bool
}

func (p treePTY) Pid() int           { return p.pid }
func (p treePTY) TreeOpen() bool     { return p.open }
func (p treePTY) Holds(pid int) bool { return p.open && pid == p.pid+1 }

// TestAnExitedAgentShellCountsUntilItsTreeCloses_REQ_ORC_004: on Windows a shell's exit does
// not end what it left in its job, which stays open until the session is closed. Until then the
// shell is still listed as an agent's and its job still names the leftover as the agent's; once
// the tree is closed, neither (delta `2026-10-windows-port`, decision 4).
func TestAnExitedAgentShellCountsUntilItsTreeCloses_REQ_ORC_004(t *testing.T) {
	t.Parallel()
	pty := &treePTY{pid: 4000, open: true}
	live := &liveSession{session: domain.Session{ID: "ses_a", OwnerThreadID: "thr_a", State: domain.StateExited}, pty: pty}
	user := &liveSession{session: domain.Session{ID: "ses_u", State: domain.StateAlive}, pty: treePTY{pid: 5000, open: true}}
	s := &Service{live: map[string]*liveSession{"ses_a": live, "ses_u": user}}

	if pids := s.AgentShellPIDs(); !slices.Equal(pids, []int{4000}) {
		t.Errorf("AgentShellPIDs = %v, want the exited agent shell whose tree is open", pids)
	}
	if got := s.AgentTreeHolding(4001); got != 4000 {
		t.Errorf("AgentTreeHolding(leftover) = %d, want the agent shell 4000", got)
	}
	if got := s.AgentTreeHolding(5001); got != 0 {
		t.Errorf("a user's shell's tree was taken for an agent's: %d", got)
	}

	pty.open = false
	if pids := s.AgentShellPIDs(); len(pids) != 0 {
		t.Errorf("AgentShellPIDs = %v after the tree closed, want none", pids)
	}
	if got := s.AgentTreeHolding(4001); got != 0 {
		t.Errorf("AgentTreeHolding after the tree closed = %d, want 0", got)
	}
}
