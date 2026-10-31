package api

import (
	"os"
	"slices"
	"sync/atomic"
	"testing"
)

// agentRefused is API §2 "An agent's terminal": every method a client inside one is refused.
var agentRefused = []string{
	"approval.respond", "layout.apply", "mcp.server.add", "mcp.server.remove", "pane.split", "rules.key.add",
	"rules.key.remove", "rules.key.rotate", "rules.reset", "rules.rollback", "session.create", "session.input",
	"skill.install", "skill.remove", "skill.set_enabled", "tab.create", "thread.hand_back", "thread.take_over",
	"thread.update", "workspace.create",
	"worktree.close_group", "worktree.discard", "worktree.merge", "worktree.reject_hunks", "worktree.write_file",
}

// TestAnAgentsTerminalCannotLandItsOwnWork_REQ_ORC_004: a client whose process runs inside an
// agent's terminal — `umb worktree merge` typed by a turn in full-auto, or anything it builds —
// is refused the methods that would land, drop or approve the agent's own work, or start a process
// outside its terminal, with PERMISSION_DENIED, whatever client kind it claims; it
// may still read. The verdict taken when it connected holds for the connection. A client outside
// every agent's terminal is served as before.
func TestAnAgentsTerminalCannotLandItsOwnWork_REQ_ORC_004(t *testing.T) {
	t.Parallel()
	engine := newFakeWorktreeEngine()
	var inside atomic.Bool
	inside.Store(true)
	var asked atomic.Int64
	s := testServerWithConfig(t, Config{
		Worktrees: engine, WorktreeThreads: &fakeWorktreeThreads{},
		AgentPeer: func(pid int) bool {
			asked.Store(int64(pid))
			return inside.Load()
		},
	})

	var marked []string
	for name, m := range s.methods {
		if m.userOnly {
			marked = append(marked, name)
		}
	}
	slices.Sort(marked)
	if !slices.Equal(marked, agentRefused) {
		t.Fatalf("userOnly methods %v, want API §2's %v", marked, agentRefused)
	}

	c := dial(t, s)
	c.hello(s.Token(), ClientTUI)
	for i, name := range agentRefused {
		resp := c.call(10+i, name, map[string]any{})
		if resp.Error == nil || resp.Error.Code != codePermissionDenied {
			t.Errorf("%s from an agent's terminal: %+v, want PERMISSION_DENIED", name, resp.Error)
		}
	}
	if asked.Load() != int64(os.Getpid()) {
		t.Fatalf("the check was asked about pid %d, want the client's %d", asked.Load(), os.Getpid())
	}
	if len(engine.merged) != 0 {
		t.Fatalf("the engine merged %v", engine.merged)
	}
	if resp := c.call(42, "worktree.get", map[string]any{"worktree_id": "wtr_1"}); resp.Error != nil {
		t.Fatalf("worktree.get from an agent's terminal: %+v", resp.Error)
	}

	// The process now reads as outside — as a child left holding the socket would — but the
	// connection was an agent's when it started.
	inside.Store(false)
	if resp := c.call(43, "worktree.merge", map[string]any{"worktree_id": "wtr_1", "revision": "rev1"}); resp.Error == nil || resp.Error.Code != codePermissionDenied {
		t.Fatalf("worktree.merge on a connection that began in an agent's terminal: %+v", resp.Error)
	}

	outside := dial(t, s)
	outside.hello(s.Token(), ClientTUI)
	if resp := outside.call(2, "worktree.merge", map[string]any{"worktree_id": "wtr_1", "revision": "rev1"}); resp.Error != nil {
		t.Fatalf("worktree.merge from outside: %+v", resp.Error)
	}
}

// TestAnUnreadablePeerIsTakenForAnAgents_REQ_ORC_004: a connection whose peer pid the kernel
// did not give is refused what an agent's terminal is refused, once the check is wired.
func TestAnUnreadablePeerIsTakenForAnAgents_REQ_ORC_004(t *testing.T) {
	t.Parallel()
	c := &conn{server: &Server{cfg: Config{AgentPeer: func(int) bool { return false }}}}
	if !c.fromAgent() {
		t.Fatal("a peer with no pid was taken for the user's")
	}
	c.server.cfg.AgentPeer = nil
	if c.fromAgent() {
		t.Fatal("without the check, a peer was taken for an agent's")
	}
}
