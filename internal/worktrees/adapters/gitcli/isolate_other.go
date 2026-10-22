//go:build !unix

package gitcli

import (
	"os/exec"
	"time"
)

// isolate bounds Wait on platforms without process sessions; T-F2-50 gives Windows its own.
func isolate(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }
