package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyPrivateDirRejectsAForeignRuntimeDirectory covers the runtime-directory
// fallback of changes/2026-09-api-f0-decisions: the parent is world-writable, so a
// directory anyone can enter must be refused before a token is written into it.
func TestVerifyPrivateDirRejectsAForeignRuntimeDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "umbral-1000")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatalf("create the loose directory: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("loosen the directory: %v", err)
	}

	if err := verifyPrivateDir(dir); err == nil {
		t.Error("a world-writable runtime directory was accepted; the token would be readable by anyone")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("tighten the directory: %v", err)
	}
	if err := verifyPrivateDir(dir); err != nil {
		t.Errorf("a 0700 directory owned by this user was rejected: %v", err)
	}

	// A directory that does not exist yet is fine; MkdirAll creates it at 0700.
	if err := verifyPrivateDir(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("an absent runtime directory was rejected: %v", err)
	}
}
