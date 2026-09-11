package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if code := run(t.Context(), []string{"-db", dbPath, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(-db) = %d, want 0; stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("the daemon did not create %s: %v", dbPath, err)
	}
	if !strings.Contains(stderr.String(), "database ready") {
		t.Errorf("startup was not logged; stderr: %s", stderr.String())
	}
}

// TestRunServesTheSocketUntilTheContextIsCancelled is the composition-root check that
// the daemon really binds its socket: the handshake is exercised over the real transport
// rather than against an in-process server.
func TestRunServesTheSocketUntilTheContextIsCancelled(t *testing.T) {
	var stdout, stderr bytes.Buffer

	dir := t.TempDir()
	socket := filepath.Join(dir, "umbral.sock")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{
			"-db", filepath.Join(dir, "umbral.db"),
			"-socket", socket,
		}, &stdout, &stderr)
	}()

	token := waitForToken(t, filepath.Join(dir, "token"))
	conn := waitForSocket(t, socket)
	defer func() { _ = conn.Close() }()

	req := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"system.hello","params":`+
			`{"token":%q,"client_kind":"cli","protocol_version":1}}`+"\n", token)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	var resp struct {
		Result *struct {
			ConnectionID string `json:"connection_id"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read handshake reply: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("handshake failed: %s", resp.Error)
	}
	if resp.Result == nil || resp.Result.ConnectionID == "" {
		t.Fatal("the handshake returned no connection_id")
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("run after cancellation = %d, want 0; stderr: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}

	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("the socket file survived shutdown: %v", err)
	}
}

// waitForToken waits for the daemon to write its token file.
func waitForToken(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return string(raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the daemon never wrote %s", path)
	return ""
}

// waitForSocket dials the socket until the daemon is listening.
func waitForSocket(t *testing.T, path string) net.Conn {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var dialer net.Dialer
		conn, err := dialer.DialContext(t.Context(), "unix", path)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the daemon never listened on %s", path)
	return nil
}
