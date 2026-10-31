//go:build unix

package sessions

import (
	"os"
)

// pathSeparators are the characters that make a command name a path.
const pathSeparators = "/"

// candidates is the one file a name resolves to in a PATH directory.
func candidates(path string, _ []string) []string { return []string{path} }

// runnable is an executable bit.
func runnable(_ string, info os.FileInfo) bool { return info.Mode()&0o111 != 0 }

// envKeyIs compares environment keys, which are case-sensitive on Unix.
func envKeyIs(key, want string) bool { return key == want }

// defaultShell is the user's $SHELL, or /bin/sh.
func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}
