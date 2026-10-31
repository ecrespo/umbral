package proctree

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestAWindowsChildIsFoundUnderItsParent_REQ_ORC_004 is the Windows process table: a child this
// test starts lists this process among its ancestors — what the agent's-terminal check walks —
// and appears under it with its program, which is what detection reads (REQ-XAG-001).
func TestAWindowsChildIsFoundUnderItsParent_REQ_ORC_004(t *testing.T) {
	t.Parallel()

	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	child := exec.CommandContext(t.Context(), cmdExe, "/d", "/c", "ping -n 30 127.0.0.1 >NUL")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	chain := Ancestors(child.Process.Pid)
	if len(chain) < 2 || chain[0] != child.Process.Pid || !slices.Contains(chain, os.Getpid()) {
		t.Fatalf("Ancestors(child) = %v, want the child first and this test (%d) among its parents", chain, os.Getpid())
	}

	found := false
	for _, p := range (Tree{}).Snapshot().Subtree(os.Getpid()) {
		if p.PID == child.Process.Pid {
			found = len(p.Argv) == 1 && strings.EqualFold(filepath.Base(p.Argv[0]), "cmd.exe")
		}
	}
	if !found {
		t.Errorf("the child is not listed under this process with its program")
	}
	if Ancestors(-1) != nil {
		t.Error("a pid that is not in the table has ancestors")
	}
}
