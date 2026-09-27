package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The autostart tests need something that can actually be launched as a process and that
// then listens on the socket. The real umbrald is not usable here: it needs libghostty
// and a database, and REQ-CLI-003 is about the launch-and-retry loop rather than about
// what gets launched.
//
// So the fake daemon is this test binary, re-executed through a one-line shell script
// that sets the marker variable. The script exists because the variable has to be set for
// the child only: setting it in the test process would turn every other subprocess into a
// daemon, and t.Setenv cannot be used from a parallel test anyway.
const (
	fakeDaemonEnv     = "UMBRAL_CLIENT_TEST_FAKE_DAEMON"
	fakeDaemonVersion = "0.0.0-fake"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeDaemonEnv) == "silent" {
		// Binds the socket and answers nothing: a daemon wedged during startup.
		os.Exit(runSilentDaemon(socketFromArgs(os.Args)))
	}
	if os.Getenv(fakeDaemonEnv) == "1" {
		// Not a test run: this process was started by launchDaemon.
		os.Exit(runFakeDaemon(socketFromArgs(os.Args)))
	}
	os.Exit(m.Run())
}

// buildFakeDaemon writes the launcher script and returns its path.
func buildFakeDaemon(t *testing.T, dir string) string {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	script := filepath.Join(dir, "fake-umbrald")
	body := fmt.Sprintf("#!/bin/sh\nexec env %s=1 %q \"$@\"\n", fakeDaemonEnv, self)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write the fake daemon: %v", err)
	}
	return script
}

// buildSilentDaemon is buildFakeDaemon's counterpart: it binds the socket and then never
// answers, which is what makes the autostart budget expire inside a dial.
func buildSilentDaemon(t *testing.T, dir string) string {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	script := filepath.Join(dir, "silent-umbrald")
	body := fmt.Sprintf("#!/bin/sh\nexec env %s=silent %q \"$@\"\n", fakeDaemonEnv, self)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write the silent daemon: %v", err)
	}
	return script
}

// runSilentDaemon accepts connections and reads nothing, so the client's handshake blocks
// until its deadline.
func runSilentDaemon(socket string) int {
	ln, ok := listenAndExpire(socket)
	if !ok {
		return 1
	}
	defer func() { _ = ln.Close() }()

	// Accepted connections are kept referenced and never answered. Dropping the
	// reference instead would let the runtime finalize the socket and close it, and a
	// close is an EOF, which is an answer; the point is that the client never gets one.
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return 0
		}
		held = append(held, conn)
	}
}

// socketFromArgs reads -socket by hand, because the flag package here belongs to the
// testing framework and would reject it.
func socketFromArgs(args []string) string {
	for i, a := range args {
		if a == "-socket" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// fakeDaemonLifetime bounds both fakes. They are detached processes: nothing closes them
// when the test that started them ends, so without a lifetime every run of this package
// leaves one behind, and a stale one still holding a socket path is a flaky neighbour for
// the next run. It is generous against the 3 s budget the tests exercise.
const fakeDaemonLifetime = 30 * time.Second

// runFakeDaemon answers system.hello and nothing else, which is all the autostart path
// needs to call the connection established.
func runFakeDaemon(socket string) int {
	ln, ok := listenAndExpire(socket)
	if !ok {
		return 1
	}
	defer func() { _ = ln.Close() }()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return 0
		}
		go serveFake(conn)
	}
}

// listenAndExpire binds the socket and arms the self-destruct.
func listenAndExpire(socket string) (net.Listener, bool) {
	if socket == "" {
		return nil, false
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		return nil, false
	}
	time.AfterFunc(fakeDaemonLifetime, func() { _ = ln.Close() })
	return ln, true
}

func serveFake(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	rd := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		var result any
		switch req.Method {
		case "system.hello":
			result = map[string]any{
				"daemon_version":   fakeDaemonVersion,
				"protocol_version": ProtocolVersion,
				"capabilities":     []string{},
				"connection_id":    "con_fake",
			}
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{
					"code": -32601, "message": "method not found",
					"data": map[string]any{"domain_code": "METHOD_NOT_FOUND"},
				},
			})
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}
