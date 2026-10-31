package sdk

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestAHostedAgentsJobEndsWhatItLeft_REQ_ORC_004 is decision 10 of the Windows delta: an agent
// runs in a Job Object from its first instruction, what it starts is listed as its own, and
// ending the job ends all of it, the processes the agent left running included.
func TestAHostedAgentsJobEndsWhatItLeft_REQ_ORC_004(t *testing.T) {
	t.Parallel()

	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	//nolint:gosec,noctx // a fixed program; the job, not a context, is what ends it
	cmd := exec.Command(cmdExe, "/d", "/c", "start /b ping -n 120 127.0.0.1 >NUL & ping -n 120 127.0.0.1 >NUL")
	if err := isolateTree(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tr, err := joinTree(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("joinTree: %v", err)
	}
	t.Cleanup(tr.end)

	var members []int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if members = tr.members(); len(members) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(members) < 3 || !slices.Contains(members, cmd.Process.Pid) {
		t.Fatalf("the job lists %v, want the agent (%d) and both pings", members, cmd.Process.Pid)
	}

	tr.end()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent outlived its job")
	}
	for _, pid := range members {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			continue // gone
		}
		ev, _ := windows.WaitForSingleObject(h, 5000)
		_ = windows.CloseHandle(h)
		if ev != windows.WAIT_OBJECT_0 {
			t.Errorf("process %d of the job outlived it", pid)
		}
	}
	if tr.members() != nil {
		t.Error("an ended job still lists processes")
	}
}
