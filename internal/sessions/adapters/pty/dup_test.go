//go:build unix

package pty

import (
	"os"
	"syscall"
)

// dupFile is another descriptor for f's open file, as SCM_RIGHTS delivers one.
func dupFile(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}
