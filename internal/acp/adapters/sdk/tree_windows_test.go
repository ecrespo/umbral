package sdk

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestAHostedAgentsJobEndsWhatItLeft_REQ_ORC_004 is decision 10 of the Windows delta: an agent
// runs in a Job Object from its first instruction, what it starts is held by it — what the peer
// check asks — and ending the job ends all of it, the processes the agent left running included.
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

	if tr.holds(os.Getpid()) {
		t.Fatal("the job holds the daemon itself")
	}
	if !tr.holds(cmd.Process.Pid) {
		t.Fatal("the job does not hold the agent")
	}
	var members []int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if members = childrenOf(t, uint32(cmd.Process.Pid)); len(members) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(members) < 2 {
		t.Fatalf("the agent started %v, want both pings", members)
	}
	for _, pid := range members {
		if !tr.holds(pid) {
			t.Errorf("the job does not hold the agent's child %d", pid)
		}
	}
	members = append(members, cmd.Process.Pid)

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
	if tr.holds(members[0]) {
		t.Error("an ended job still holds a process")
	}
}

// childrenOf lists the processes whose parent is pid.
func childrenOf(t *testing.T, pid uint32) []int {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var out []int
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ParentProcessID == pid && pe.ProcessID != pid {
			out = append(out, int(pe.ProcessID))
		}
	}
	return out
}
