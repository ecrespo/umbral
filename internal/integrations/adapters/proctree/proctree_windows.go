package proctree

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTable reads a Toolhelp snapshot. Windows reuses pids and keeps a parent's pid after the
// parent exits, so a parent created after its child is a stranger holding a reused pid: the link
// is dropped and the walk stops there (delta `2026-10-windows-port`, decision 4).
func processTable() []entry {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var out []entry
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		out = append(out, entry{pid: int(pe.ProcessID), ppid: int(pe.ParentProcessID)})
	}
	created := map[int]int64{}
	for _, e := range out {
		created[e.pid] = creationTime(e.pid)
	}
	for i, e := range out {
		child, parent := created[e.pid], created[e.ppid]
		if e.ppid == e.pid || child == 0 || parent == 0 || parent > child {
			out[i].ppid = 0
		}
	}
	return out
}

// creationTime is when pid started, in 100 ns ticks; 0 when it cannot be read.
func creationTime(pid int) int64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	return creation.Nanoseconds() / 100
}

// argvOf is the process's executable alone: Windows keeps a command line in the process's own
// memory, which this does not read, so detection matches on the program.
func argvOf(pid int) []string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return nil
	}
	return []string{windows.UTF16ToString(buf[:n])}
}
