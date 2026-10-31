//go:build unix

package skills

import (
	"os"
	"syscall"
)

// hardlinked reports whether the file has another name somewhere.
func hardlinked(info os.FileInfo) bool {
	//nolint:unconvert // Nlink is uint64 on Linux and uint16 on macOS
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(st.Nlink) > 1
}

// openEntry opens a bundle entry for reading. O_NOFOLLOW and O_NONBLOCK: a file swapped for a
// symlink is not followed, and one swapped for a FIFO cannot block the handler on open.
func openEntry(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- a file inside the bundle the user asked to inspect
}
