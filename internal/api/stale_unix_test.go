//go:build unix

package api

import (
	"net"
	"path/filepath"
	"testing"
)

// TestStaleSocketIsReplaced covers the crash-restart path: a leftover socket file must
// not stop the daemon from starting.
func TestStaleSocketIsReplaced(t *testing.T) {
	t.Parallel()

	dir := socketDir(t)
	socket := filepath.Join(dir, SocketFileName)

	var lc net.ListenConfig
	first, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatalf("create a stale socket: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close the stale listener: %v", err)
	}

	s, err := Listen(t.Context(), Config{
		SocketPath: socket, TokenPath: filepath.Join(dir, TokenFileName), DaemonVersion: "t",
	})
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
