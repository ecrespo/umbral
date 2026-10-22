package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestUmbBinPathPrefersTheSibling_REQ_INT_001: UMBRAL_BIN_PATH is the `umb` beside `umbrald`
// first, then the one on PATH, and nothing when there is neither.
func TestUmbBinPathPrefersTheSibling_REQ_INT_001(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	self := filepath.Join(dir, "umbrald")
	onPath := func(string) (string, error) { return "/usr/local/bin/umb", nil }
	nowhere := func(string) (string, error) { return "", errors.New("not found") }

	if got := findUmbFrom(self, onPath); got != "/usr/local/bin/umb" {
		t.Errorf("with no sibling: %q, want the one on PATH", got)
	}
	if got := findUmbFrom(self, nowhere); got != "" {
		t.Errorf("with neither: %q, want nothing", got)
	}
	sibling := filepath.Join(dir, "umb")
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findUmbFrom(self, nowhere); got != "" {
		t.Errorf("a sibling that is not executable was taken: %q", got)
	}
	if err := os.Chmod(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := findUmbFrom(self, onPath); got != sibling {
		t.Errorf("with a sibling: %q, want %q", got, sibling)
	}
}
