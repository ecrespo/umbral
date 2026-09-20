package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUmbAutostartFailsWith69_REQ_CLI_003 is the unwanted half of REQ-CLI-003: the daemon
// is not running, cannot be started, and the client gives up inside the 3 s budget with
// an error the CLI maps to exit code 69.
//
// The daemon is made unstartable by pointing DaemonPath at a file that is not there,
// rather than by emptying PATH: an empty PATH would also break the fallback that looks
// beside the running binary, so the test would pass for a reason it is not about.
func TestUmbAutostartFailsWith69_REQ_CLI_003(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "umbral.sock")

	start := time.Now()
	c, err := Connect(context.Background(), Options{
		SocketPath: socket,
		DaemonPath: filepath.Join(t.TempDir(), "no-such-umbrald"),
	})
	elapsed := time.Since(start)

	if err == nil {
		_ = c.Close()
		t.Fatal("Connect succeeded with no daemon and no way to start one")
	}
	if !errors.Is(err, ErrDaemonUnavailable) {
		t.Fatalf("error = %v, want one that unwraps to ErrDaemonUnavailable so the CLI exits 69", err)
	}
	if elapsed > StartTimeout+time.Second {
		t.Errorf("Connect took %v; REQ-CLI-003 budgets %v", elapsed, StartTimeout)
	}

	// The message is part of the requirement: "exit with code 69 and an actionable
	// message". A message that does not name the socket leaves the user with nothing to
	// try next.
	if msg := err.Error(); !strings.Contains(msg, socket) {
		t.Errorf("the error does not name the socket the user would have to fix: %q", msg)
	}
}

// TestAutostartLaunchesTheDaemon_REQ_CLI_003 is the wanted half: nothing is listening, so
// the client starts the daemon and connects to it.
//
// The daemon here is a shell script that binds the socket, because the real umbrald needs
// libghostty and a database; what REQ-CLI-003 is about is the launch-and-retry, not what
// gets launched.
func TestAutostartLaunchesTheDaemon_REQ_CLI_003(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "umbral.sock")
	token := "3f1c" // the fake accepts anything; the client still has to send one
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0o600); err != nil {
		t.Fatalf("write the token: %v", err)
	}

	// A tiny Go program is overkill here; a listener started by the test and a fake
	// "daemon" that only has to exist would not exercise the launch. So the fake daemon
	// is this test binary re-invoked in a mode that binds the socket.
	fake := buildFakeDaemon(t, dir)

	c, err := Connect(context.Background(), Options{SocketPath: socket, DaemonPath: fake})
	if err != nil {
		t.Fatalf("Connect did not start the daemon: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.DaemonVersion != fakeDaemonVersion {
		t.Errorf("daemon_version = %q, want %q: the handshake did not complete",
			c.DaemonVersion, fakeDaemonVersion)
	}
}

// TestConnectDoesNotAutostartWhenAskedNotTo covers the flag a script uses when spawning a
// daemon would be wrong: the failure must be immediate rather than after the 3 s budget.
func TestConnectDoesNotAutostartWhenAskedNotTo(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, err := Connect(context.Background(), Options{
		SocketPath:  filepath.Join(t.TempDir(), "umbral.sock"),
		NoAutostart: true,
	})
	if err == nil {
		t.Fatal("Connect succeeded with no daemon and autostart disabled")
	}
	if !errors.Is(err, ErrNoSocket) {
		t.Errorf("error = %v, want ErrNoSocket", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v before failing; with autostart off there is nothing to wait for", elapsed)
	}
}

// TestConnectDoesNotAutostartOnARealError checks the discrimination the autostart depends
// on: a daemon that is listening and rejects the handshake is a diagnosable failure, and
// starting a second daemon on the same socket would turn it into an intermittent one.
func TestConnectDoesNotAutostartOnARealError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "umbral.sock")
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("tok"), 0o600); err != nil {
		t.Fatalf("write the token: %v", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept and hang up without answering: a daemon that is there but broken.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	// DaemonPath points at nothing, so an attempted autostart would fail with
	// ErrDaemonUnavailable. Getting anything else proves no autostart was attempted.
	_, err = Connect(context.Background(), Options{
		SocketPath: socket,
		DaemonPath: filepath.Join(dir, "no-such-umbrald"),
	})
	if err == nil {
		t.Fatal("Connect succeeded against a daemon that hangs up")
	}
	if errors.Is(err, ErrDaemonUnavailable) {
		t.Errorf("Connect tried to autostart over a listening daemon: %v", err)
	}
}

// TestAutostartBudgetExpiringMidDialStillReports69_REQ_CLI_003 covers the case the retry
// loop alone gets wrong: the daemon is launched, binds the socket and then never answers,
// so the 3 s budget runs out inside a dial rather than between two of them. The dial fails
// with a context deadline, which is not ErrNoSocket, and reporting it raw would exit 1 —
// telling a script the request was malformed when the truth is that the daemon never came
// up. REQ-CLI-003 measures the wait, so the answer is 69 either way.
func TestAutostartBudgetExpiringMidDialStillReports69_REQ_CLI_003(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "umbral.sock")
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("tok"), 0o600); err != nil {
		t.Fatalf("write the token: %v", err)
	}

	// Nothing is listening yet, so Connect autostarts. The "daemon" it starts binds the
	// socket and then goes silent, which is what a daemon wedged during startup looks
	// like from here.
	fake := buildSilentDaemon(t, dir)

	start := time.Now()
	_, err := Connect(context.Background(), Options{SocketPath: socket, DaemonPath: fake})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Connect succeeded against a daemon that never answers")
	}
	if !errors.Is(err, ErrDaemonUnavailable) {
		t.Errorf("error = %v, want one that unwraps to ErrDaemonUnavailable so the CLI exits 69", err)
	}
	if elapsed > StartTimeout+2*time.Second {
		t.Errorf("Connect took %v; REQ-CLI-003 budgets %v", elapsed, StartTimeout)
	}
}
