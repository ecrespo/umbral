package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// testServer starts a server on a socket inside a temporary directory and returns it
// with its token.
func testServer(t *testing.T, status StatusFunc) *Server {
	t.Helper()

	dir := t.TempDir()
	s, err := Listen(t.Context(), Config{
		SocketPath:    filepath.Join(dir, SocketFileName),
		TokenPath:     filepath.Join(dir, TokenFileName),
		DaemonVersion: "0.1.0-test",
		Status:        status,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})
	return s
}

// client is a minimal NDJSON JSON-RPC peer for the tests.
type client struct {
	// t is nil when the client is driven by a benchmark, which has no *testing.T. Every
	// helper that needs it is only called from tests.
	t    *testing.T
	conn net.Conn
	dec  *json.Decoder
}

func dial(t *testing.T, s *Server) *client {
	t.Helper()

	var dialer net.Dialer
	c, err := dialer.DialContext(t.Context(), "unix", s.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &client{t: t, conn: c, dec: json.NewDecoder(c)}
}

// call sends a request and reads the reply.
func (c *client) call(id int, method string, params any) response {
	c.t.Helper()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	c.send(append(body, '\n'))
	return c.read()
}

func (c *client) send(raw []byte) {
	c.t.Helper()

	if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		c.t.Fatalf("set write deadline: %v", err)
	}
	if _, err := c.conn.Write(raw); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *client) read() response {
	c.t.Helper()

	if err := c.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		c.t.Fatalf("set read deadline: %v", err)
	}
	var resp response
	if err := c.dec.Decode(&resp); err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return resp
}

// expectClosed asserts the daemon hung up, which the spec requires after UNAUTHORIZED.
func (c *client) expectClosed() {
	c.t.Helper()

	if err := c.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		c.t.Fatalf("set read deadline: %v", err)
	}
	var discard json.RawMessage
	err := c.dec.Decode(&discard)
	if err == nil {
		c.t.Fatalf("the connection stayed open and delivered %s", discard)
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			c.t.Fatal("the connection was not closed; the read timed out instead")
		}
	}
}

func (c *client) hello(token string, kind ClientKind) response {
	return c.call(1, "system.hello", map[string]any{
		"token": token, "client_kind": string(kind),
		"client_version": "0.1.0-test", "protocol_version": ProtocolVersion,
	})
}

// TestSocketPermissions0600_REQ_SEC_007 checks that no other local user can reach the
// daemon, which is the whole of REQ-SEC-007.
func TestSocketPermissions0600_REQ_SEC_007(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)

	info, err := os.Stat(s.SocketPath())
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != socketMode {
		t.Errorf("socket mode = %#o, want %#o", got, socketMode)
	}

	// The token beside it is a credential; the same rule applies (API Spec §2).
	tokenInfo, err := os.Stat(filepath.Join(filepath.Dir(s.SocketPath()), TokenFileName))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := tokenInfo.Mode().Perm(); got != tokenMode {
		t.Errorf("token mode = %#o, want %#o", got, tokenMode)
	}
}

// TestHelloRejectsBadToken_REQ_SEC_003 is the core of REQ-SEC-003: a wrong token gets
// UNAUTHORIZED and the connection is closed, so the socket is not a token oracle.
func TestHelloRejectsBadToken_REQ_SEC_003(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.hello("0000000000000000000000000000000000000000000000000000000000000000", ClientTUI)
	if resp.Error == nil {
		t.Fatalf("a bad token was accepted: %+v", resp.Result)
	}
	if resp.Error.Code != codeUnauthorized {
		t.Errorf("error code = %d, want %d (UNAUTHORIZED)", resp.Error.Code, codeUnauthorized)
	}
	if resp.Error.Data == nil || resp.Error.Data.DomainCode != "UNAUTHORIZED" {
		t.Errorf("domain code = %+v, want UNAUTHORIZED", resp.Error.Data)
	}
	c.expectClosed()
}

// TestMethodBeforeHelloIsUnauthorized_REQ_SEC_003 covers step 3 of API Spec §2: any call
// that precedes the handshake is refused and the connection is closed.
func TestMethodBeforeHelloIsUnauthorized_REQ_SEC_003(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.call(1, "system.status", nil)
	if resp.Error == nil || resp.Error.Code != codeUnauthorized {
		t.Fatalf("system.status before hello = %+v, want UNAUTHORIZED", resp)
	}
	c.expectClosed()
}

// TestUnknownMethodBeforeHelloDoesNotLeakTheMethodTable pins the ordering: an
// unauthenticated peer must not be able to tell a real method from a made-up one.
func TestUnknownMethodBeforeHelloDoesNotLeakTheMethodTable(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.call(1, "session.create", nil)
	if resp.Error == nil || resp.Error.Code != codeUnauthorized {
		t.Fatalf("unknown method before hello = %+v, want UNAUTHORIZED", resp)
	}
}

func TestHelloSucceedsAndReturnsAConnectionID(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.hello(s.Token(), ClientTUI)
	if resp.Error != nil {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	var result helloResult
	decodeResult(t, resp, &result)
	if result.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol_version = %d, want %d", result.ProtocolVersion, ProtocolVersion)
	}
	if result.DaemonVersion != "0.1.0-test" {
		t.Errorf("daemon_version = %q, want the configured one", result.DaemonVersion)
	}
	if len(result.ConnectionID) < 5 || result.ConnectionID[:4] != "con_" {
		t.Errorf("connection_id = %q, want a con_ prefixed ULID (Art. 6)", result.ConnectionID)
	}
	// Derived from the method table rather than written by hand: the namespaces advertised
	// are exactly the ones with registered methods. Announcing one whose methods do not
	// exist would tell a client to take a branch that cannot work, which is the mistake
	// this assertion exists to catch, so it is updated when a namespace is really added.
	if want := []string{"block", "session"}; !slices.Equal(result.Capabilities, want) {
		t.Errorf("capabilities = %v, want %v", result.Capabilities, want)
	}
}

func TestHelloRejectsAnUnsupportedProtocolVersion(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.call(1, "system.hello", map[string]any{
		"token": s.Token(), "client_kind": "tui", "protocol_version": 99,
	})
	if resp.Error == nil || resp.Error.Code != codeUnsupportedProtocolVersion {
		t.Fatalf("protocol 99 = %+v, want UNSUPPORTED_PROTOCOL_VERSION", resp)
	}
	if resp.Error.Data == nil || len(resp.Error.Data.Supported) == 0 {
		t.Error("the error does not report the supported versions, which API Spec §2 requires")
	}
}

func TestHelloRejectsAnUnknownClientKind(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.call(1, "system.hello", map[string]any{
		"token": s.Token(), "client_kind": "robot", "protocol_version": ProtocolVersion,
	})
	if resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Fatalf("client_kind robot = %+v, want VALIDATION_ERROR", resp)
	}
	if len(resp.Error.Data.Details) == 0 || resp.Error.Data.Details[0].Field != "client_kind" {
		t.Errorf("the error does not name the offending field: %+v", resp.Error.Data)
	}
}

func TestStatusReportsWhatTheDaemonSupplies(t *testing.T) {
	t.Parallel()

	s := testServer(t, func(context.Context) (StatusResult, error) {
		return StatusResult{SessionsAlive: 3, ThreadsRunning: 1}, nil
	})
	c := dial(t, s)

	if resp := c.hello(s.Token(), ClientCLI); resp.Error != nil {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	resp := c.call(2, "system.status", nil)
	if resp.Error != nil {
		t.Fatalf("system.status failed: %+v", resp.Error)
	}

	var status StatusResult
	decodeResult(t, resp, &status)
	if status.SessionsAlive != 3 || status.ThreadsRunning != 1 {
		t.Errorf("status = %+v, want the counts the daemon supplied", status)
	}
	if status.DaemonVersion != "0.1.0-test" {
		t.Errorf("daemon_version = %q, want the server's own", status.DaemonVersion)
	}
	if status.Providers == nil || status.MCP == nil {
		t.Error("providers and mcp must be empty arrays, not null, so clients can range over them")
	}
}

func TestUnknownMethodAfterHello(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	resp := c.call(2, "no.such.method", nil)
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method = %+v, want METHOD_NOT_FOUND", resp)
	}
	// The connection survives an unknown method; only auth failures close it.
	if resp := c.call(3, "system.status", nil); resp.Error != nil {
		t.Errorf("the connection did not survive an unknown method: %+v", resp.Error)
	}
}

func TestInvalidJSONIsAParseError(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	c.send([]byte("{not json}\n"))
	resp := c.read()
	if resp.Error == nil || resp.Error.Code != codeParseError {
		t.Fatalf("invalid JSON = %+v, want PARSE_ERROR", resp)
	}
}

func TestNonJSONRPCMessageIsAnInvalidRequest(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	c.send([]byte(`{"id":1,"method":"system.hello"}` + "\n"))
	resp := c.read()
	if resp.Error == nil || resp.Error.Code != codeInvalidRequest {
		t.Fatalf("missing jsonrpc field = %+v, want INVALID_REQUEST", resp)
	}
}

// TestOversizedMessageIsRejected covers the 4 MiB framing limit of API Spec §1.
func TestOversizedMessageIsRejected(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	huge := bytes.Repeat([]byte("a"), MaxMessageBytes+1024)
	c.send(append(huge, '\n'))

	resp := c.read()
	if resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Fatalf("oversized message = %+v, want VALIDATION_ERROR", resp)
	}
}

// TestStaleSocketIsReplaced covers the crash-restart path: a leftover socket file must
// not stop the daemon from starting.
func TestStaleSocketIsReplaced(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
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

	dir := t.TempDir()
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
func TestTokenIsReusedAcrossRestarts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := Config{
		SocketPath:    filepath.Join(dir, SocketFileName),
		TokenPath:     filepath.Join(dir, TokenFileName),
		DaemonVersion: "t",
	}

	first, err := Listen(t.Context(), cfg)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	token := first.Token()
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Listen(t.Context(), cfg)
	if err != nil {
		t.Fatalf("second Listen: %v", err)
	}
	defer func() { _ = second.Close() }()

	if second.Token() != token {
		t.Error("the token changed across a restart; every running client would be logged out")
	}
	if len(token) != tokenBytes*2 {
		t.Errorf("token length = %d hex characters, want %d", len(token), tokenBytes*2)
	}
}

// TestLoadOrCreateTokenRepairsPermissions covers a token that became world-readable, for
// example after a careless chmod: it is a credential, so the mode is fixed on load.
func TestLoadOrCreateTokenRepairsPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), TokenFileName)
	if _, err := LoadOrCreateToken(path); err != nil {
		t.Fatalf("create the token: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen the permissions: %v", err)
	}
	if _, err := LoadOrCreateToken(path); err != nil {
		t.Fatalf("reload the token: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := info.Mode().Perm(); got != tokenMode {
		t.Errorf("token mode after reload = %#o, want %#o", got, tokenMode)
	}
}

func TestMethodAllowsRestrictsByClientKind(t *testing.T) {
	t.Parallel()

	open := method{}
	if !open.allows(ClientCLI) {
		t.Error("a method with no kinds must be open to every client")
	}

	tuiOnly := method{kinds: []ClientKind{ClientTUI, ClientDesktop}}
	if tuiOnly.allows(ClientCLI) {
		t.Error("the CLI reached a method restricted to tui and desktop")
	}
	if !tuiOnly.allows(ClientTUI) {
		t.Error("the TUI was refused a method it is allowed to call")
	}
}

// decodeResult re-decodes a response result into a typed value.
func decodeResult(t *testing.T, resp response, into any) {
	t.Helper()

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}

// TestTokenFileWithZeroLengthIsRewrittenAt0600_REQ_SEC_003 covers a token file that
// exists but is empty, left behind by a crashed first run or a stray touch. os.WriteFile
// does not apply its mode to an existing file, so the token used to inherit that file's
// permissions; a world-readable token is REQ-SEC-003 defeated before a client connects.
func TestTokenFileWithZeroLengthIsRewrittenAt0600_REQ_SEC_003(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), TokenFileName)
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatalf("create the empty token file: %v", err)
	}

	token, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if len(token) != tokenBytes*2 {
		t.Errorf("token length = %d hex characters, want %d", len(token), tokenBytes*2)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := info.Mode().Perm(); got != tokenMode {
		t.Errorf("token mode = %#o, want %#o", got, tokenMode)
	}
}

// TestHelloWithBadTokenAndBadClientKindStillCloses_REQ_SEC_003 pins the ordering inside
// the handshake. REQ-SEC-003 has no exceptions: a connection that does not present a
// valid token gets UNAUTHORIZED and is closed, so no other validation may answer first
// and leave the connection open for another attempt.
func TestHelloWithBadTokenAndBadClientKindStillCloses_REQ_SEC_003(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.call(1, "system.hello", map[string]any{
		"token": "not-the-token", "client_kind": "robot", "protocol_version": ProtocolVersion,
	})
	if resp.Error == nil || resp.Error.Code != codeUnauthorized {
		t.Fatalf("bad token with a bad client_kind = %+v, want UNAUTHORIZED", resp)
	}
	c.expectClosed()
}

// TestSecondHelloIsRefusedAndCloses covers a re-handshake on an authenticated
// connection: it is a protocol violation, and refusing it also stops a live session from
// being used to grind tokens.
func TestSecondHelloIsRefusedAndCloses(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("first handshake failed: %+v", resp.Error)
	}

	resp := c.hello(s.Token(), ClientTUI)
	if resp.Error == nil || resp.Error.Code != codeUnauthorized {
		t.Fatalf("second handshake = %+v, want UNAUTHORIZED", resp)
	}
	c.expectClosed()
}

// TestHelloRequiresTheProtocolVersion checks that a missing protocol_version is rejected
// rather than assumed compatible: API Spec §2 always carries the field, and guessing
// would silently pair a v1 daemon with a client built for something else.
func TestHelloRequiresTheProtocolVersion(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	resp := c.call(1, "system.hello", map[string]any{
		"token": s.Token(), "client_kind": "tui",
	})
	if resp.Error == nil || resp.Error.Code != codeUnsupportedProtocolVersion {
		t.Fatalf("hello without protocol_version = %+v, want UNSUPPORTED_PROTOCOL_VERSION", resp)
	}
}

// TestErrorsWithAnUndeterminedIDCarryNull covers JSON-RPC 2.0 §5: when the id cannot be
// read, the reply must carry an explicit null rather than omit the field.
func TestErrorsWithAnUndeterminedIDCarryNull(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)
	c := dial(t, s)

	c.send([]byte("{not json}\n"))
	resp := c.read()
	if resp.Error == nil || resp.Error.Code != codeParseError {
		t.Fatalf("invalid JSON = %+v, want PARSE_ERROR", resp)
	}
	if string(resp.ID) != "null" {
		t.Errorf("id = %q, want null", string(resp.ID))
	}
}

// TestCapabilitiesFollowTheMethodTable proves the advertisement is derived rather than
// hand-maintained, so it cannot drift away from what the daemon can actually serve.
func TestCapabilitiesFollowTheMethodTable(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)

	// system.* is never advertised: every client may always call it.
	if slices.Contains(s.capabilities(), "system") {
		t.Errorf("capabilities = %v, must not list system", s.capabilities())
	}

	// The list follows the table rather than a hand-written constant, so registering a
	// method in a new namespace is enough to advertise it.
	s.methods["block.list"] = method{}
	got := s.capabilities()
	if !slices.Contains(got, "block") || !slices.Contains(got, "session") {
		t.Errorf("capabilities = %v, want both block and session", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("capabilities = %v, want them sorted so the handshake is stable", got)
	}

	// Removing every method of a namespace removes it from the advertisement.
	for name := range s.methods {
		if strings.HasPrefix(name, "session.") {
			delete(s.methods, name)
		}
	}
	if slices.Contains(s.capabilities(), "session") {
		t.Errorf("capabilities = %v, still lists session with no session method registered",
			s.capabilities())
	}
}
