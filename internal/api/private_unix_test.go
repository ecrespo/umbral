//go:build unix

package api

import (
	"os"
	"path/filepath"
	"testing"
)

// assertPrivate fails unless path is mode 0600.
func assertPrivate(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != tokenMode {
		t.Errorf("%s mode = %#o, want %#o", filepath.Base(path), got, tokenMode)
	}
}

// loosen makes path readable by other users, as a careless chmod would.
func loosen(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen the permissions: %v", err)
	}
}

// TestSocketPermissions0600_REQ_SEC_007 checks that no other local user can reach the
// daemon, which is the whole of REQ-SEC-007.
func TestSocketPermissions0600_REQ_SEC_007(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)

	info, err := os.Stat(s.SocketPath())
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != socketMode {
		t.Errorf("socket mode = %#o, want %#o", got, socketMode)
	}

	// The token beside it is a credential; the same rule applies (API Spec §2).
	tokenInfo, err := os.Stat(filepath.Join(filepath.Dir(s.SocketPath()), TokenFileName))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := tokenInfo.Mode().Perm(); got != tokenMode {
		t.Errorf("token mode = %#o, want %#o", got, tokenMode)
	}
}
