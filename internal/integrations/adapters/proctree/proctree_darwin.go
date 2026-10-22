package proctree

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func processTable() []entry {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	out := make([]entry, 0, len(procs))
	for _, p := range procs {
		out = append(out, entry{pid: int(p.Proc.P_pid), ppid: int(p.Eproc.Ppid)})
	}
	return out
}

// argvOf reads kern.procargs2: argc as a 32-bit integer, the executable's path, NUL padding,
// then argc NUL-terminated arguments and the environment, which is not read.
func argvOf(pid int) []string {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argv := make([]string, 0, argc)
	for len(argv) < argc && len(rest) > 0 {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			end = len(rest)
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[min(end+1, len(rest)):]
	}
	return argv
}
