package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestCallSkipsNotificationsWaitingForItsReply is why `call` loops instead of reading one
// frame. The daemon may publish a notification on this connection at any moment — the
// stream is bidirectional and shared — and a client that took the next frame as its answer
// would decode a `session.output` into whatever it asked for and report a wrong result
// rather than an error.
func TestCallSkipsNotificationsWaitingForItsReply(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		if method == "system.hello" {
			_ = enc.Encode(helloReply(id))
			return
		}
		// Two notifications first, then the reply. A single-frame read would take the
		// first notification as the answer.
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "method": "session.output",
			"params": map[string]any{"session_id": "ses_x", "seq": 1, "data_b64": "aGk="},
		})
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "method": "block.started",
			"params": map[string]any{"id": "blk_x"},
		})
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"daemon_version": "0.0.0-fake", "sessions_alive": 7},
		})
	})

	c, err := Dial(context.Background(), socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	var out struct {
		SessionsAlive int `json:"sessions_alive"`
	}
	if err := c.Call(context.Background(), "system.status", nil, &out); err != nil {
		t.Fatalf("system.status: %v", err)
	}
	if out.SessionsAlive != 7 {
		t.Errorf("sessions_alive = %d, want 7: the client took a notification for its reply",
			out.SessionsAlive)
	}
}

// TestDialSurfacesTheDaemonsError checks that a rejected handshake reaches the caller as
// the daemon's own domain code rather than as a transport failure. `umb` branches on that
// code, and REQ-SEC-003 is the case a user is most likely to hit: a token file from a
// previous installation.
func TestDialSurfacesTheDaemonsError(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"error": map[string]any{
				"code": -32001, "message": "unauthorized",
				"data": map[string]any{"domain_code": "UNAUTHORIZED"},
			},
		})
	})

	_, err := Dial(context.Background(), socket)
	if err == nil {
		t.Fatal("Dial succeeded against a daemon that answered UNAUTHORIZED")
	}
	if got := DomainCode(err); got != "UNAUTHORIZED" {
		t.Errorf("domain code = %q, want UNAUTHORIZED; the caller cannot tell a bad token from a broken socket", got)
	}
	// A rejected handshake is not a missing daemon: autostarting over it would spawn a
	// second daemon for a problem a second daemon does not solve.
	if errors.Is(err, ErrNoSocket) {
		t.Error("a rejected handshake was reported as ErrNoSocket, which would trigger an autostart")
	}
}

// TestDialWithoutATokenLooksLikeAMissingDaemon covers a fresh machine: the daemon has
// never run, so there is no token file. That has to reach Connect as ErrNoSocket, or
// `umb status` on a new install would print a complaint about a file the user has never
// heard of instead of starting the daemon.
func TestDialWithoutATokenLooksLikeAMissingDaemon(t *testing.T) {
	t.Parallel()

	_, err := Dial(context.Background(), filepath.Join(t.TempDir(), "umbral.sock"))
	if !errors.Is(err, ErrNoSocket) {
		t.Errorf("error = %v, want ErrNoSocket so the autostart path runs", err)
	}
}

// TestFrameLimitIsEnforced stops a daemon that went wrong from making the client allocate
// without bound. API Spec §8 caps a message at 4 MiB in both directions.
func TestFrameLimitIsEnforced(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		if method == "system.hello" {
			_ = enc.Encode(helloReply(id))
			return
		}
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"blob": strings.Repeat("x", maxMessageBytes+1)},
		})
	})

	c, err := Dial(context.Background(), socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	var out map[string]any
	err = c.Call(context.Background(), "system.status", nil, &out)
	if err == nil {
		t.Fatal("a frame past the 4 MiB limit was accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %v, want one naming the frame limit", err)
	}
}

func helloReply(id json.RawMessage) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id,
		"result": map[string]any{
			"daemon_version": "0.0.0-fake", "protocol_version": ProtocolVersion,
			"capabilities": []string{}, "connection_id": "con_fake",
		},
	}
}

// fakeServer starts a socket whose handler decides what to answer, and writes the token
// file the client reads. It returns the socket path.
func fakeServer(t *testing.T, handle func(method string, id json.RawMessage, enc *json.Encoder)) string {
	t.Helper()
	return fakeServerWithParams(t, func(method string, _ map[string]any, id json.RawMessage, enc *json.Encoder) {
		handle(method, id, enc)
	})
}

// fakeServerWithParams is fakeServer with the request's parameters handed to the handler,
// so a test can assert on what the client sent and not only on what it did with the reply.
func fakeServerWithParams(t *testing.T, handle func(method string, params map[string]any, id json.RawMessage, enc *json.Encoder)) string {
	t.Helper()

	dir := t.TempDir()
	socket := filepath.Join(dir, "umbral.sock")
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("write the token: %v", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
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
						Params map[string]any  `json:"params"`
					}
					if err := json.Unmarshal(line, &req); err != nil {
						return
					}
					handle(req.Method, req.Params, req.ID, enc)
				}
			}()
		}
	}()
	return socket
}

// TestCloseRacingACallIsSafe holds the type to its own promise. `Close` and `Call` both
// touch the connection pointer, so without a shared lock the race detector finds them
// even though the failure is rare enough to never show up by hand.
func TestCloseRacingACallIsSafe(t *testing.T) {
	t.Parallel()

	socket := fakeServer(t, func(method string, id json.RawMessage, enc *json.Encoder) {
		_ = enc.Encode(helloReply(id))
	})

	c, err := Dial(context.Background(), socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.Call(context.Background(), "system.status", nil, nil) }()
		go func() { defer wg.Done(); _ = c.Close() }()
	}
	wg.Wait()
}

// TestConnectAnnouncesTheRequestedClientKind_REQ_SEC_003 is the regression test for a bug
// that cost a real-daemon debugging session: every connection announced `cli`, and API
// Spec §2 keeps `session.*` out of the `cli` method set, so the TUI's first call came back
// METHOD_NOT_FOUND — indistinguishable from a daemon that does not implement it.
//
// No fake caught it because every fake answered the handshake without reading it.
func TestConnectAnnouncesTheRequestedClientKind_REQ_SEC_003(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{name: "default is cli", want: ClientKindCLI},
		{name: "tui asks for tui", opts: Options{ClientKind: ClientKindTUI}, want: ClientKindTUI},
		{name: "cli asks for cli", opts: Options{ClientKind: ClientKindCLI}, want: ClientKindCLI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := make(chan string, 1)
			socket := fakeServerReadingHello(t, seen)

			opts := tc.opts
			opts.SocketPath = socket
			opts.NoAutostart = true
			c, err := Connect(context.Background(), opts)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer func() { _ = c.Close() }()

			if got := <-seen; got != tc.want {
				t.Errorf("client_kind = %q, want %q; the daemon serves a different method set to each", got, tc.want)
			}
		})
	}
}

// fakeServerReadingHello answers the handshake and reports the client_kind it was sent.
func fakeServerReadingHello(t *testing.T, seen chan<- string) string {
	t.Helper()
	return fakeServerWithParams(t, func(method string, params map[string]any, id json.RawMessage, enc *json.Encoder) {
		if method == "system.hello" {
			kind, _ := params["client_kind"].(string)
			select {
			case seen <- kind:
			default:
			}
			_ = enc.Encode(helloReply(id))
		}
	})
}
