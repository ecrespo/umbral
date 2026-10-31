package config

import (
	"path/filepath"
	"testing"

	"github.com/Microsoft/go-winio"
)

// TestAPipeOfTheCurrentUserIsTrusted_REQ_WIN_001: a pipe this user serves passes the check a
// client makes before its handshake. One another account serves is refused the same way, by
// its server's SID; the runner has no second account to prove that half with.
func TestAPipeOfTheCurrentUserIsTrusted_REQ_WIN_001(t *testing.T) {
	name := PipeName(filepath.Join(t.TempDir(), SocketFileName))
	ln, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer func() { _ = c.Close() }()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	conn, err := winio.DialPipeContext(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := VerifyPipeServer(conn); err != nil {
		t.Fatalf("this user's own pipe was refused: %v", err)
	}
}
