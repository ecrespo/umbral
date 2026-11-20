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
	// The listener is closed only after its one Accept has returned. go-winio v0.6.2 hangs in
	// Close when it races an Accept whose client has already gone: the accept loop takes the
	// close signal, reads ERROR_NO_DATA from the dead client, and waits for another client
	// with nobody left to wake it. That hung this test for ten minutes on a runner (T-HRD-13's
	// kind of failure), so the client stays connected until the server holds it.
	accepted, served := make(chan struct{}), make(chan struct{})
	defer func() {
		<-served
		_ = ln.Close()
	}()
	go func() {
		defer close(served)
		c, err := ln.Accept()
		close(accepted)
		if err == nil {
			defer func() { _ = c.Close() }()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	conn, err := winio.DialPipeContext(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	// Deferred after the listener's, so it runs first: the client hangs up once the server has
	// accepted it, which ends the server's read.
	defer func() {
		<-accepted
		_ = conn.Close()
	}()
	if err := VerifyPipeServer(conn); err != nil {
		t.Fatalf("this user's own pipe was refused: %v", err)
	}
}
