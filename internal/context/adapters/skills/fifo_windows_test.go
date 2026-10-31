package skills

import "testing"

// mkfifo reports that Windows has no FIFO in a directory: that case does not arise there.
func mkfifo(*testing.T, string) bool { return false }
