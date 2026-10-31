//go:build unix

package api

import (
	"net"
	"os"
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

// TestListenRefusesToDeleteARegularFile guards the stale-socket cleanup: a mistyped path
// must never make the daemon delete the user's data.
func TestListenRefusesToDeleteARegularFile(t *testing.T) {
	t.Parallel()

	dir := socketDir(t)
	path := filepath.Join(dir, "important.txt")
	if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write the decoy file: %v", err)
	}

	if _, err := Listen(t.Context(), Config{
		SocketPath: path, TokenPath: filepath.Join(dir, TokenFileName), DaemonVersion: "t",
	}); err == nil {
		t.Fatal("Listen accepted a regular file as its socket path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the decoy file was removed: %v", err)
	}
}

// TestTokenIsReusedAcrossRestarts matters because clients cache it: regenerating the
// token on every start would log every running client out.
