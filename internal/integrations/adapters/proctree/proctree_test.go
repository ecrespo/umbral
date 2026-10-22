//go:build linux || darwin

package proctree

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/integrations/domain"
)

// TestSubtreeFindsAnAgentUnderTheShell_REQ_XAG_001: the subtree of the test's own process starts
// with that process, then lists a child whose argv[0] is `opencode` with its argv, before the
// grandchild it starts — breadth first — and the built-in manifests recognise it. The child is
// a shell renamed through argv[0], because a multi-call `sleep` refuses to run under another
// name; it runs in a process group of its own so the cleanup kills its `sleep` too.
func TestSubtreeFindsAnAgentUnderTheShell_REQ_XAG_001(t *testing.T) {
	agent := exec.CommandContext(t.Context(), "/bin/sh", "-c", "sleep 30; true")
	agent.Args[0] = "/opt/agents/opencode"
	agent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-agent.Process.Pid, syscall.SIGKILL)
		_ = agent.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		procs := Tree{}.Snapshot().Subtree(os.Getpid())
		if len(procs) == 0 || procs[0].PID != os.Getpid() {
			t.Fatalf("the subtree does not start at its root: %+v", procs)
		}
		agentAt, childAt := -1, -1
		for i, p := range procs {
			switch {
			case p.PID == agent.Process.Pid:
				agentAt = i
				if len(p.Argv) != 3 || p.Argv[0] != "/opt/agents/opencode" || p.Argv[2] != "sleep 30; true" {
					t.Fatalf("argv %q", p.Argv)
				}
			case len(p.Argv) == 2 && p.Argv[0] == "sleep" && p.Argv[1] == "30":
				childAt = i
			}
		}
		if agentAt >= 0 && childAt > agentAt {
			d, ok := domain.Detect(domain.Builtins(), procs[agentAt:agentAt+1])
			if !ok || d.Manifest.Name != "opencode" {
				t.Fatalf("detection %+v, %v", d, ok)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent (%d) and its child were not both listed in order: %+v", agent.Process.Pid, procs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSubtreeOfANonexistentProcessIsEmpty_REQ_XAG_001(t *testing.T) {
	t.Parallel()
	if got := (Tree{}).Snapshot().Subtree(1 << 30); len(got) != 0 {
		t.Errorf("subtree %+v", got)
	}
}
