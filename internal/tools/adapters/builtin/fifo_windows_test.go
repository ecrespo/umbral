package builtin

import "testing"

// mkfifo makes nothing: Windows has no FIFO in a directory, and the missing path is refused
// like one.
func mkfifo(*testing.T, string) {}
