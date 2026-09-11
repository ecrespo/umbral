package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// File names inside the runtime directory (API Spec §2 and Metadata).
const (
	SocketFileName = "umbral.sock"
	TokenFileName  = "token"
)

// tokenBytes is the token length from API Spec §2: 32 random bytes, written as hex.
const tokenBytes = 32

// Directory and file modes. The runtime directory is 0700 and both the socket and the
// token file are 0600, which is REQ-SEC-007 and the second half of REQ-SEC-003: a token
// another local user can read is not a credential.
const (
	runtimeDirMode = 0o700
	socketMode     = 0o600
	tokenMode      = 0o600
)

// RuntimeDir reports the directory holding the socket and the token.
//
// On Linux it is $XDG_RUNTIME_DIR/umbral, a tmpfs the kernel clears at logout. On macOS
// there is no such variable, so the API Spec metadata places it under Application
// Support instead.
func RuntimeDir() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "umbral"), nil
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("api: locate the runtime directory: %w", err)
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

// verifyPrivateDir rejects a pre-existing runtime directory that the current user does
// not own or that others can enter. A directory that does not exist yet is fine: MkdirAll
// will create it with mode 0700.
func verifyPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("api: inspect the runtime directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("api: %s exists and is not a directory", dir)
	}
	if got := info.Mode().Perm(); got != runtimeDirMode {
		return fmt.Errorf("api: runtime directory %s is %#o, want %#o; refusing to put a token there",
			dir, got, runtimeDirMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("api: cannot read the owner of %s", dir)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("api: runtime directory %s is owned by uid %d, not %d",
			dir, stat.Uid, os.Getuid())
	}
	return nil
}

// DefaultSocketPath is the socket the daemon binds when no path is configured.
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

// LoadOrCreateToken returns the per-installation token, creating it on first run.
//
// An existing token is reused so that clients already holding it keep working across
// daemon restarts. Its permissions are repaired rather than trusted: a token that became
// world-readable is a credential leak, and refusing to start would leave the user with a
// daemon that will not run and no obvious fix.
//
// A file that exists but is empty, left by a crashed first run or by a stray `touch`,
// is treated as absent and rewritten. It is truncated through OpenFile rather than
// os.WriteFile because WriteFile does not apply its mode argument to a file that already
// exists: the token would silently inherit whatever mode that file had.
func LoadOrCreateToken(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), runtimeDirMode); err != nil {
		return "", fmt.Errorf("api: create the runtime directory: %w", err)
	}

	//nolint:gosec // path comes from the operator's -socket flag or the runtime directory, never from a remote peer
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && len(existing) > 0:
		if err := os.Chmod(path, tokenMode); err != nil {
			return "", fmt.Errorf("api: fix the token file permissions: %w", err)
		}
		return string(existing), nil
	case err != nil && !os.IsNotExist(err):
		return "", fmt.Errorf("api: read the token file: %w", err)
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("api: generate a token: %w", err)
	}
	token := hex.EncodeToString(raw)

	if err := writeTokenFile(path, token); err != nil {
		return "", err
	}
	return token, nil
}

// writeTokenFile writes the token at mode 0600, then reads the mode back. The explicit
// Chmod covers the case where the file already existed, where O_CREATE's mode is ignored;
// the read-back covers a filesystem that silently refuses the mode, which would leave a
// credential readable without anyone noticing.
func writeTokenFile(path, token string) error {
	//nolint:gosec // path comes from the operator's -socket flag or the runtime directory, never from a remote peer
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, tokenMode)
	if err != nil {
		return fmt.Errorf("api: create the token file: %w", err)
	}
	if _, err := f.WriteString(token); err != nil {
		_ = f.Close()
		return fmt.Errorf("api: write the token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("api: close the token file: %w", err)
	}

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
