//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// verifyPrivateDir rejects a pre-existing runtime directory that the current user does
// not own or that others can enter. A directory that does not exist yet is fine: MkdirAll
// will create it with mode 0700.
func verifyPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: inspect the runtime directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("config: %s exists and is not a directory", dir)
	}
	if got := info.Mode().Perm(); got != RuntimeDirMode {
		return fmt.Errorf("config: runtime directory %s is %#o, want %#o; refusing to put a token there",
			dir, got, RuntimeDirMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("config: cannot read the owner of %s", dir)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("config: runtime directory %s is owned by uid %d, not %d",
			dir, stat.Uid, os.Getuid())
	}
	return nil
}
