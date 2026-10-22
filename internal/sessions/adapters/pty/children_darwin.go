package pty

import "golang.org/x/sys/unix"

// childrenOf lists the processes whose parent is pid, from the kernel's process table. It
// used to run `pgrep -P`, whose start-up on a loaded CI runner could outlast the 300 ms the
// cancel allowed it, leaving the command's children alive.
func childrenOf(pid int) []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	var out []int
	for _, p := range procs {
		if int(p.Eproc.Ppid) == pid {
			out = append(out, int(p.Proc.P_pid))
		}
	}
	return out
}
