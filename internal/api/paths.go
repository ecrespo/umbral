package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/config"
)

// tokenBytes is the token length from API Spec §2: 32 random bytes, written as hex.
const tokenBytes = 32

// File modes. A token or a socket another local user can read is not a credential:
// tokenMode is the second half of REQ-SEC-003 and socketMode is REQ-SEC-007.
const (
	tokenMode  = 0o600
	socketMode = 0o600
)

// Where the socket and the token live is config's business, because the client needs the
// same answer and cannot import this package (Tech Design §5.2).
const (
	SocketFileName = config.SocketFileName
	TokenFileName  = config.TokenFileName
)

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
	if err := os.MkdirAll(filepath.Dir(path), config.RuntimeDirMode); err != nil {
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
