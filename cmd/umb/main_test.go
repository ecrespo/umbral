package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/ecrespo/umbral/internal/api"
)

// TestBlockLastJSONFollowsTheBlockSchema_REQ_CLI_002 is the second half of REQ-CLI-002:
// "print the last closed block of the current session as JSON, following the API `Block`
// schema".
//
// The expected key set is taken from `internal/api`'s own `Block` type rather than
// written out here, so this test compares the two sides of the socket instead of
// comparing the CLI against a copy of the schema that could drift with it. A field added
// to the daemon and forgotten in the CLI fails here, which is the drift worth catching:
// whatever is parsing `--json` sees the CLI's answer, not the daemon's.
func TestBlockLastJSONFollowsTheBlockSchema_REQ_CLI_002(t *testing.T) {
	t.Parallel()

	want := jsonFieldNames(reflect.TypeOf(api.Block{}))
	got := jsonFieldNames(reflect.TypeOf(block{}))

	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Errorf("`umb block last --json` would print\n  %v\nbut API Spec §4 Block is\n  %v",
			got, want)
	}
}

// TestBlockLastPrintsWhatTheDaemonSent_REQ_CLI_002 runs the command end to end against a
// daemon that answers `block.get`, and checks the bytes on stdout.
//
// It asserts on the decoded object rather than on the exact string, because the field
// order of a JSON object is not part of the contract; what is, is that every key of the
// schema is present with the daemon's value, including the ones whose value is null. A
// consumer writing `.exit_code` in jq needs the key to exist even when the block was
// abandoned.
func TestBlockLastPrintsWhatTheDaemonSent_REQ_CLI_002(t *testing.T) {
	t.Parallel()

	sent := map[string]any{
		"id": "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", "session_id": "ses_01J9Z3K8T2QH6W4V5X7Y8Z9A0B",
		"origin": "user", "thread_id": nil, "command": "go test ./...", "cwd": "/home/u/repo",
		"host": "thinkpad", "state": "finished", "exit_code": 1,
		"started_at": 1757592001000, "ended_at": 1757592005200, "duration_ms": 4200,
		"output_bytes": 1834, "output_truncated": false,
	}
	socket := serveFakeDaemon(t, map[string]any{"block.get": sent})

	var stdout, stderr bytes.Buffer
	code := run([]string{"block", "last", "--json", "--socket", socket, "--no-autostart"},
		&stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	for _, key := range jsonFieldNames(reflect.TypeOf(api.Block{})) {
		if _, ok := got[key]; !ok {
			t.Errorf("the printed object has no %q; a consumer reading it in jq gets null with no way to tell it apart from an absent block", key)
		}
	}
	if got["command"] != "go test ./..." || got["exit_code"] != float64(1) {
		t.Errorf("the printed block is not the one the daemon sent: %v", got)
	}
	if got["thread_id"] != nil {
		t.Errorf("thread_id = %v, want null preserved", got["thread_id"])
	}

	// One object, one line: `umb ... --json` has to compose with a shell loop.
	if n := strings.Count(strings.TrimRight(stdout.String(), "\n"), "\n"); n != 0 {
		t.Errorf("stdout spans %d extra lines; --json prints one object per line", n)
	}
}

// TestBlockLastUsesTheSessionFromTheEnvironment_REQ_CLI_002 covers "of the current
// session". The daemon injects UMBRAL_SESSION_ID into every managed pane (Tech Design
// §5.2b), and without forwarding it the command would answer with the last block of some
// other terminal, which is a wrong answer rather than a missing one.
func TestBlockLastUsesTheSessionFromTheEnvironment_REQ_CLI_002(t *testing.T) {
	seen := make(chan map[string]any, 1)
	socket := serveFakeDaemonWithParams(t, map[string]any{
		"block.get": map[string]any{
			"id": "blk_x", "session_id": "ses_env", "origin": "user", "thread_id": nil,
			"command": "ls", "cwd": "/", "host": "h", "state": "finished", "exit_code": 0,
			"started_at": 1, "ended_at": 2, "duration_ms": 1,
			"output_bytes": 0, "output_truncated": false,
		},
	}, seen)

	t.Setenv("UMBRAL_SESSION_ID", "ses_env")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"block", "last", "--json", "--socket", socket, "--no-autostart"},
		&stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	params := <-seen
	if params["session_id"] != "ses_env" {
		t.Errorf("session_id = %v, want the UMBRAL_SESSION_ID of the pane", params["session_id"])
	}
	if params["block_id"] != "last" {
		t.Errorf("block_id = %v, want the reserved id \"last\"", params["block_id"])
	}
}

// TestBlockLastExits69WhenTheDaemonIsUnavailable_REQ_CLI_003 is the CLI's half of
// REQ-CLI-003: the client's failure has to become exit code 69 rather than a generic 1,
// because that is what a script branches on.
func TestBlockLastExits69WhenTheDaemonIsUnavailable_REQ_CLI_003(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"block", "last", "--json", "--no-autostart",
		"--socket", filepath.Join(t.TempDir(), "umbral.sock"),
	}, &stdout, &stderr)

	if code != exitUnavailable {
		t.Errorf("exit = %d, want %d (EX_UNAVAILABLE)", code, exitUnavailable)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q; a failed --json call must print nothing parsable", stdout.String())
	}
	if !strings.Contains(stderr.String(), "umb:") {
		t.Errorf("stderr = %q, want a message naming the tool", stderr.String())
	}
}

// jsonFieldNames reports the JSON keys a struct marshals to, skipping the ones marked
// omitempty: those are the optional extras of `block.get` (output_plain, output_raw_b64)
// rather than part of the Block schema.
func jsonFieldNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(opts, "omitempty") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// serveFakeDaemon starts a socket that answers the handshake and the given methods.
func serveFakeDaemon(t *testing.T, results map[string]any) string {
	t.Helper()
	return serveFakeDaemonWithParams(t, results, nil)
}

// serveFakeDaemonWithParams also forwards each request's params, so a test can assert on
// what the CLI asked for and not only on what it printed.
func serveFakeDaemonWithParams(t *testing.T, results map[string]any, seen chan<- map[string]any, watchHello ...bool) string {
	t.Helper()

	dir := t.TempDir()
	socket := filepath.Join(dir, "umbral.sock")
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("write the token: %v", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, results, seen, len(watchHello) > 0 && watchHello[0])
		}
	}()
	return socket
}

// serveFakeDaemonWatchingHello forwards the handshake params instead of skipping them.
func serveFakeDaemonWatchingHello(t *testing.T, seen chan<- map[string]any) string {
	t.Helper()
	return serveFakeDaemonWithParams(t, map[string]any{"system.status": map[string]any{
		"daemon_version": "0.0.0-fake", "uptime_ms": 1,
		"sessions_alive": 0, "threads_running": 0,
	}}, seen, true)
}

func serveConn(conn net.Conn, results map[string]any, seen chan<- map[string]any, watchHello bool) {
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
		if seen != nil && (watchHello == (req.Method == "system.hello")) {
			select {
			case seen <- req.Params:
			default:
			}
		}

		var result any
		if req.Method == "system.hello" {
			result = map[string]any{
				"daemon_version": "0.0.0-fake", "protocol_version": 1,
				"capabilities": []string{"block"}, "connection_id": "con_fake",
			}
		} else if r, ok := results[req.Method]; ok {
			result = r
		} else {
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

// failingWriter reports a full disk on every write.
type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// TestWriteFailureIsNotReportedAsSuccess_REQ_CLI_004 covers the case a script cannot recover from
// otherwise: `umb block last --json > /full/disk` exits 0 while the file holds nothing, so
// whatever reads that file believes it has the block.
func TestWriteFailureIsNotReportedAsSuccess_REQ_CLI_004(t *testing.T) {
	t.Parallel()

	socket := serveFakeDaemon(t, map[string]any{"block.get": map[string]any{
		"id": "blk_x", "session_id": "ses_x", "origin": "user", "thread_id": nil,
		"command": "ls", "cwd": "/", "host": "h", "state": "finished", "exit_code": 0,
		"started_at": 1, "ended_at": 2, "duration_ms": 1,
		"output_bytes": 0, "output_truncated": false,
	}})

	var stderr bytes.Buffer
	code := run([]string{"block", "last", "--json", "--socket", socket, "--no-autostart"},
		failingWriter{err: errors.New("no space left on device")}, &stderr)

	if code == exitOK {
		t.Error("a command whose output never reached the disk exited 0")
	}
	if !strings.Contains(stderr.String(), "no space left") {
		t.Errorf("stderr = %q, want the write error", stderr.String())
	}
}

// TestBrokenPipeIsNotAFailure_REQ_CLI_004 is the other half: `umb status | head -1` closes the pipe on
// purpose, and reporting that as an error would make every such pipeline look broken.
func TestBrokenPipeIsNotAFailure_REQ_CLI_004(t *testing.T) {
	t.Parallel()

	socket := serveFakeDaemon(t, map[string]any{"system.status": map[string]any{
		"daemon_version": "0.0.0-fake", "uptime_ms": 1000,
		"sessions_alive": 0, "threads_running": 0,
	}})

	var stderr bytes.Buffer
	code := run([]string{"status", "--socket", socket, "--no-autostart"},
		failingWriter{err: syscall.EPIPE}, &stderr)

	if code != exitOK {
		t.Errorf("exit = %d, want 0: a closed pipe is what `| head` does, not a failure", code)
	}
}

// TestAutostartFailureExits69_REQ_CLI_003 joins the two halves of REQ-CLI-003 in one test
// of the binary: the daemon is not running, `umb` tries to start it, the attempt cannot
// succeed, and the whole thing becomes exit 69 with a message. The other exit-69 test
// passes --no-autostart, so it only proves the ErrNoSocket mapping and never reaches the
// launch path that the requirement is actually about.
func TestAutostartFailureExits69_REQ_CLI_003(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"status",
		"--socket", filepath.Join(dir, "umbral.sock"),
		"--daemon-path", filepath.Join(dir, "no-such-umbrald"),
	}, &stdout, &stderr)

	if code != exitUnavailable {
		t.Errorf("exit = %d, want %d (EX_UNAVAILABLE)", code, exitUnavailable)
	}
	if !strings.Contains(stderr.String(), "umbrald") {
		t.Errorf("stderr = %q, want a message naming the daemon the user has to install", stderr.String())
	}
	// Naming the path proves --daemon-path reached the client. Without that assertion the
	// test passes even when the flag is dropped, because `umbrald` is not on PATH under
	// `go test` either and the lookup fails anyway.
	if !strings.Contains(stderr.String(), "no-such-umbrald") {
		t.Errorf("stderr = %q, want it to name the --daemon-path that was tried", stderr.String())
	}
}

// TestHandshakeCarriesWhatTheSpecRequires_REQ_SEC_003 checks the bytes `umb` puts on the
// wire for `system.hello`. The other tests all answer the handshake without looking at it,
// so a wrong `client_kind` would go unnoticed here and turn every `block.*` call into
// METHOD_NOT_FOUND against a real daemon, which is the API Spec §2 allowlist doing its job
// against a client that misidentified itself.
func TestHandshakeCarriesWhatTheSpecRequires_REQ_SEC_003(t *testing.T) {
	t.Parallel()

	seen := make(chan map[string]any, 1)
	socket := serveFakeDaemonWatchingHello(t, seen)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "--socket", socket, "--no-autostart"},
		&stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	params := <-seen
	if params["client_kind"] != "cli" {
		t.Errorf("client_kind = %v, want \"cli\" (API Spec §2)", params["client_kind"])
	}
	if params["protocol_version"] != float64(1) {
		t.Errorf("protocol_version = %v, want 1; the daemon rejects a handshake without it",
			params["protocol_version"])
	}
	if tok, _ := params["token"].(string); tok != "deadbeef" {
		t.Errorf("token = %q, want the one from the token file beside the socket", tok)
	}
	if v, _ := params["client_version"].(string); v == "" {
		t.Error("client_version is empty; the daemon logs what its clients claim to be")
	}
}

// TestAPISchemaPrintsTheProtocol_REQ_API_004 drives the command end to end.
//
// The daemon is not running and must not need to be: the schema describes the binary, and a
// client that has to reach a daemon before it can find out what the protocol is cannot use
// it for the thing REQ-API-004 names — validating against it. So this test asserts the
// absence of a socket is not a problem, which is a claim about the command rather than about
// the document.
func TestAPISchemaPrintsTheProtocol_REQ_API_004(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"api", "schema", "--json"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitOK, stderr.String())
	}

	var doc struct {
		ProtocolVersion int `json:"protocol_version"`
		Methods         []struct {
			Name string `json:"name"`
		} `json:"methods"`
		Errors []struct {
			DomainCode string `json:"domain_code"`
			Code       int    `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, stdout.String())
	}
	if doc.ProtocolVersion != 1 {
		t.Errorf("protocol_version = %d, want 1", doc.ProtocolVersion)
	}

	var found bool
	for _, m := range doc.Methods {
		if m.Name == "block.search" {
			found = true
		}
	}
	if !found {
		t.Errorf("the schema names %d methods and none of them is block.search", len(doc.Methods))
	}
	for _, e := range doc.Errors {
		if e.DomainCode == "METHOD_NOT_FOUND" && e.Code != -32601 {
			t.Errorf("METHOD_NOT_FOUND = %d, want -32601", e.Code)
		}
	}
}

// TestAPISchemaRequiresJSON_REQ_API_004 refuses rather than inventing a second format.
func TestAPISchemaRequiresJSON_REQ_API_004(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"api", "schema"}, &stdout, &stderr); code == exitOK {
		t.Errorf("`umb api schema` without --json exited 0 and printed %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--json") {
		t.Errorf("stderr does not say what is missing: %q", stderr.String())
	}
}
