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

// endTreeAtExit is true: the job is closed when the agent exits, ending what it left.
const endTreeAtExit = true

// signal does nothing: end, which closes the job, is what terminates it.
func (t *tree) signal() {}

// end closes the job, which terminates everything in it.
func (t *tree) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// holds reports whether pid runs in the job. A process that cannot be asked about is reported
// held while the job is open, so the peer check fails closed.
func (t *tree) holds(pid int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return true
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var in int32
	if r, _, _ := isProcessInJob.Call(uintptr(h), uintptr(t.job), uintptr(unsafe.Pointer(&in))); r == 0 {
		return true
	}
	return in != 0
}

// isProcessInJob is kernel32's IsProcessInJob, which x/sys/windows does not wrap.
var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// openNonblocking opens p for reading; a directory holds no FIFO on Windows.
func openNonblocking(p string) (*os.File, error) {
	return os.Open(p) //nolint:gosec // a git file in the thread's own directory tree
}
