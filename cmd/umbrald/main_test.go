package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunVersionPrintsVersion covers the only behaviour the T-F0-01 skeleton has:
// -version writes the build version to stdout and exits 0.
func TestRunVersionPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run(t.Context(), []string{"-version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(-version) = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got == "" {
		t.Fatal("run(-version) wrote nothing to stdout")
	}
	if stderr.Len() != 0 {
		t.Errorf("run(-version) wrote to stderr: %s", stderr.String())
	}
}

// TestRunRejectsUnknownFlag pins the EX_USAGE contract: a bad command line exits 64
// and never writes to stdout.
func TestRunRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run(t.Context(), []string{"-no-such-flag"}, &stdout, &stderr); code != exitUsage {
		t.Errorf("run(-no-such-flag) = %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("run(-no-such-flag) wrote to stdout: %s", stdout.String())
	}
}

// TestRunOpensTheDatabaseAndRecovers is the composition-root smoke test: the daemon
// creates its database, migrates it and runs recovery without a pre-existing file.
func TestRunOpensTheDatabaseAndRecovers(t *testing.T) {
	var stdout, stderr bytes.Buffer

	dbPath := filepath.Join(t.TempDir(), "nested", "umbral.db")
	if code := run(t.Context(), []string{"-db", dbPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(-db) = %d, want 0; stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("the daemon did not create %s: %v", dbPath, err)
	}
	if !strings.Contains(stderr.String(), "umbrald started") {
		t.Errorf("startup was not logged; stderr: %s", stderr.String())
	}
}
