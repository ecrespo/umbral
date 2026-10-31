// Runtime locations for the daemon socket and its token. Duplicating this in the client
// would duplicate verifyPrivateDir, which is a security check, and two copies of a
// security check are one copy too many.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// File names inside the runtime directory (API Spec §2 and Metadata).
const (
	SocketFileName = "umbral.sock"
	TokenFileName  = "token"
)

// RuntimeDirMode is 0700 on the directory holding the socket and the token. Together with
// the 0600 on both files it is REQ-SEC-007 and the second half of REQ-SEC-003: a token
// another local user can read is not a credential.
const RuntimeDirMode = 0o700

// RuntimeDir reports the directory holding the socket and the token.
//
// On Linux it is $XDG_RUNTIME_DIR/umbral, a tmpfs the kernel clears at logout. On macOS
// there is no such variable, so the API Spec metadata places it under Application
// Support instead.
func RuntimeDir() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "umbral"), nil
	}
	if runtime.GOOS == "windows" {
		return windowsDir("LOCALAPPDATA", "run")
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: locate the runtime directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "Umbral"), nil
	}
	// A Linux session without XDG_RUNTIME_DIR, such as a bare `su`, still needs a
	// private place to put a 0600 socket. The API Spec names no such fallback, so this
	// is recorded in changes/_archive/2026-09-api-f0-decisions/.
	//
	// The parent here is world-writable, so an existing directory is only accepted when
	// the current user owns it and nobody else can enter it. Without that check another
	// local user could pre-create it and read the token, which is the whole of
	// REQ-SEC-003 defeated before the daemon starts.
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("umbral-%d", os.Getuid()))
	if err := verifyPrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// DefaultSocketPath is the socket the daemon binds, and the one a client dials, when no
// path is configured.
func DefaultSocketPath() (string, error) { return runtimePath(SocketFileName) }

// DefaultTokenPath is the token file that sits beside the default socket.
func DefaultTokenPath() (string, error) { return runtimePath(TokenFileName) }

func runtimePath(name string) (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// windowsDir is %<base>%\Umbral\<sub>, where Windows keeps a user's own files (delta
// `2026-10-windows-port`, decision 2).
func windowsDir(base, sub string) (string, error) {
	root := os.Getenv(base)
	if root == "" {
		return "", fmt.Errorf("config: %%%s%% is not set", base)
	}
	return filepath.Join(root, "Umbral", sub), nil
}
