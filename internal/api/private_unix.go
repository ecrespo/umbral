//go:build unix

package api

import (
	"fmt"
	"os"
)

// securePrivateFile sets mode 0600 and reads it back: a filesystem that silently refuses the
// mode would leave a credential readable without anyone noticing.
func securePrivateFile(path string) error {
	if err := os.Chmod(path, tokenMode); err != nil {
		return fmt.Errorf("api: set the token file permissions: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("api: verify the token file permissions: %w", err)
	}
	if got := info.Mode().Perm(); got != tokenMode {
		return fmt.Errorf("api: token file is %#o, want %#o", got, tokenMode)
	}
	return nil
}
