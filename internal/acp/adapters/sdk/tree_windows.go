package sdk

import (
	"errors"
	"os"
	"os/exec"
)

// errNoJob refuses a hosted agent on Windows until it runs in a Job Object that ends everything
// it starts (delta `2026-10-windows-port`, decision 10): without one, what it leaves behind
// would outlive it unseen, which REQ-ORC-004 does not allow.
var errNoJob = errors.New("hosted ACP agents are not supported on Windows yet")

func isolateTree(*exec.Cmd) error { return errNoJob }

func killGroup(int) {}

// openNonblocking opens p for reading; a directory holds no FIFO on Windows.
func openNonblocking(p string) (*os.File, error) {
	return os.Open(p) //nolint:gosec // a git file in the thread's own directory tree
}
