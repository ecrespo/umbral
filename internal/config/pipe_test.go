package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEachRuntimeDirectoryHasItsOwnPipe_REQ_WIN_001: a client that knows the socket's path
// reaches its daemon's pipe, and two runtime directories never share one.
func TestEachRuntimeDirectoryHasItsOwnPipe_REQ_WIN_001(t *testing.T) {
	a := PipeName(filepath.Join("a", "run", SocketFileName))
	if a != PipeName(filepath.Join("a", "run", TokenFileName)) {
		t.Error("two files of one runtime directory name different pipes")
	}
	if a == PipeName(filepath.Join("b", "run", SocketFileName)) {
		t.Error("two runtime directories share a pipe")
	}
	if !strings.HasPrefix(a, `\\.\pipe\umbral-`) || len(a) != len(`\\.\pipe\umbral-`)+16 {
		t.Errorf("PipeName = %q", a)
	}
}
