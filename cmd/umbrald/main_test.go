package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunVersionPrintsVersion covers the only behaviour the T-F0-01 skeleton has:
// -version writes the build version to stdout and exits 0.
func TestRunVersionPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := run([]string{"-version"}, &stdout, &stderr); code != 0 {
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

	if code := run([]string{"-no-such-flag"}, &stdout, &stderr); code != exitUsage {
		t.Errorf("run(-no-such-flag) = %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("run(-no-such-flag) wrote to stdout: %s", stdout.String())
	}
}
