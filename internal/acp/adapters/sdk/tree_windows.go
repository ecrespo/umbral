package sdk

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isolateTree starts the agent suspended, so it runs nothing before joinTree has put it in its
// Job Object: a child started before that would escape the job (delta `2026-10-windows-port`,
// decision 10).
func isolateTree(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return nil
}

// tree is the agent's Job Object. Closing it terminates every process in it, which is what ends
// the shells and servers the agent started; until then, what the agent left in it is listed as
// the agent's own for REQ-ORC-004.
type tree struct {
	mu  sync.Mutex
	job windows.Handle
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// joinTree puts the suspended agent in a new job that kills its processes when closed, then
// lets it run.
func joinTree(pid int) (*tree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create a job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure the job object: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("open the agent's process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("put the agent in its job: %w", err)
	}
	if status, _, _ := ntResumeProcess.Call(uintptr(h)); status != 0 {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("resume the agent: NTSTATUS %#x", status)
	}
	return &tree{job: job}, nil
}

// end closes the job, which terminates everything in it.
func (t *tree) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// members lists the processes in the job, the agent's own and what it left.
func (t *tree) members() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return nil
	}
	// JOBOBJECT_BASIC_PROCESS_ID_LIST: two DWORD counts, then one ULONG_PTR pid per process.
	type idList struct {
		assigned, listed uint32
		pids             [1024]uintptr
	}
	var list idList
	if err := windows.QueryInformationJobObject(t.job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil {
		return nil
	}
	n := min(int(list.listed), len(list.pids))
	out := make([]int, 0, n)
	for _, pid := range list.pids[:n] {
		out = append(out, int(pid))
	}
	return out
}

// openNonblocking opens p for reading; a directory holds no FIFO on Windows.
func openNonblocking(p string) (*os.File, error) {
	return os.Open(p) //nolint:gosec // a git file in the thread's own directory tree
}
