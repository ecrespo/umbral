package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestWorkspaceCreatePrintsTheTree_REQ_CLI_005 covers the core of REQ-CLI-005: "WHEN
// `umb workspace` … runs with one of the subcommands the daemon serves, THE SYSTEM SHALL
// invoke the JSON-RPC method of the same name and print its result", and "SHALL print the
// daemon's answer as JSON WHERE `--json` is given".
//
// `workspace.create` answers with three objects at once (API Spec §5.4), and the tree is
// the whole point of the command: a script that creates a workspace needs the tab and the
// root pane it got, or its next command — `umb pane split w1:p1` — has nothing to address.
// So the test checks that all three survive the printer, in both output shapes, and that
// the directory given positionally is the `cwd` the daemon received.
func TestWorkspaceCreatePrintsTheTree_REQ_CLI_005(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	sent := map[string]any{
		"workspace": map[string]any{
			"id": "w1", "label": "repo", "cwd": cwd, "order_index": 0,
			"rollup_state": "unknown", "tab_ids": []any{"w1:t1"},
			"created_at": 1757592000000, "closed_at": nil,
		},
		"tab": map[string]any{
			"id": "w1:t1", "workspace_id": "w1", "label": "main", "order_index": 0,
			"focused_pane_id": "w1:p1", "created_at": 1757592000000, "closed_at": nil,
		},
		"root_pane": map[string]any{
			"id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1",
			"session_id": "ses_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", "thread_id": nil,
			"label": "", "cwd": cwd, "aliases": []any{"w1:p1"},
			"attention_state": "unknown", "state_source": nil, "metadata": map[string]any{},
			"created_at": 1757592000000, "closed_at": nil,
		},
	}
	reply := map[string]fakeReply{
		"workspace.create": func(map[string]any) (any, string) { return sent, "" },
	}

	t.Run("json", func(t *testing.T) {
		t.Parallel()
		d := serveRecordingDaemon(t, reply)

		var stdout, stderr bytes.Buffer
		code := run([]string{
			"workspace", "create", cwd, "--json",
			"--socket", d.socket, "--no-autostart",
		}, &stdout, &stderr)
		requireExit(t, code, exitOK, &stderr)

		calls := d.callsTo("workspace.create")
		if len(calls) != 1 {
			t.Fatalf("workspace.create was called %d times, want exactly 1: the command is one call to the method of the same name (decision 1)", len(calls))
		}
		if calls[0].Params["cwd"] != cwd {
			t.Errorf("params.cwd = %v, want the positional %q", calls[0].Params["cwd"], cwd)
		}

		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v (%q)", err, stdout.String())
		}
		for _, key := range []string{"workspace", "tab", "root_pane"} {
			assertCarries(t, key, got[key], sent[key])
		}
	})

	t.Run("human", func(t *testing.T) {
		t.Parallel()
		d := serveRecordingDaemon(t, reply)

		var stdout, stderr bytes.Buffer
		code := run([]string{
			"workspace", "create", cwd,
			"--socket", d.socket, "--no-autostart",
		}, &stdout, &stderr)
		requireExit(t, code, exitOK, &stderr)

		out := stdout.String()
		if strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Errorf("stdout without --json is a JSON object (%q); JSON is only WHERE --json is given", out)
		}
		for _, id := range []string{"w1", "w1:t1", "w1:p1"} {
			if !strings.Contains(out, id) {
				t.Errorf("stdout = %q does not name %s; the user is left without the identifier the next command needs", out, id)
			}
		}
	})
}

// TestPaneSplitAddressesByPublicId_REQ_CLI_005 covers "addressing workspaces, tabs and panes
// by the public identifiers of REQ-WS-002 given as positional arguments", and "SHALL use the
// exit codes of REQ-CLI-004".
//
// `umb pane split w1:p1` is the command Art. 6's exception is written for, so the positional
// identifier has to reach `pane.split` as `pane_id`, verbatim. An alias left behind by a move
// (REQ-WS-007) follows the same grammar, and resolving it is the daemon's job (decision 2):
// the fake daemon here resolves `w1:p3` to the pane now called `w2:p1`, and the test checks
// both spellings reach that pane. A CLI that validated or resolved identifiers itself — by
// listing panes first, say — would reject the alias or send a second call; either fails here.
//
// The exit-code branches are the REQ-CLI-004 triple: 0 when the daemon answers, 1 when it
// answers with an error, 69 when there is no daemon to answer.
func TestPaneSplitAddressesByPublicId_REQ_CLI_005(t *testing.T) {
	t.Parallel()

	// w1:p3 was moved to w2 and is now w2:p1; its identifier from w1 stays an alias.
	resolve := map[string]string{"w1:p1": "w1:p1", "w2:p1": "w2:p1", "w1:p3": "w2:p1"}
	split := func(params map[string]any) (any, string) {
		id, _ := params["pane_id"].(string)
		target, ok := resolve[id]
		if !ok {
			return nil, "NOT_FOUND"
		}
		ws, _, _ := strings.Cut(target, ":")
		return map[string]any{
			"pane": map[string]any{
				"id": ws + ":p9", "tab_id": ws + ":t1", "workspace_id": ws,
				"session_id": "ses_01J9Z3K8T2QH6W4V5X7Y8Z9A0D", "thread_id": nil,
				"label": "", "cwd": "/repo", "aliases": []any{ws + ":p9"},
				"attention_state": "unknown", "state_source": nil, "metadata": map[string]any{},
				"created_at": 1757592000000, "closed_at": nil,
			},
			"layout": map[string]any{
				"workspace_id": ws, "tab_id": ws + ":t1", "focused_pane_id": ws + ":p9",
				"root": map[string]any{
					"type": "split", "direction": params["direction"], "ratio": 0.5,
					"first":  map[string]any{"type": "pane", "pane_id": target, "label": "", "cwd": "/repo"},
					"second": map[string]any{"type": "pane", "pane_id": ws + ":p9", "label": "", "cwd": "/repo"},
				},
			},
		}, ""
	}

	for _, tc := range []struct {
		name, id, wantResolved string
	}{
		{"current identifier", "w1:p1", "w1:p1"},
		{"moved pane by its new identifier", "w2:p1", "w2:p1"},
		{"moved pane by its alias", "w1:p3", "w2:p1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := serveRecordingDaemon(t, map[string]fakeReply{"pane.split": split})

			var stdout, stderr bytes.Buffer
			code := run([]string{
				"pane", "split", tc.id, "--direction", "right", "--json",
				"--socket", d.socket, "--no-autostart",
			}, &stdout, &stderr)
			requireExit(t, code, exitOK, &stderr)

			if other := d.callsExceptHello(); len(other) != 1 || other[0].Method != "pane.split" {
				t.Fatalf("the daemon received %v, want exactly one pane.split: the CLI does not resolve identifiers itself (DD-001, decision 2)", methodsOf(other))
			}
			params := d.callsTo("pane.split")[0].Params
			if params["pane_id"] != tc.id {
				t.Errorf("params.pane_id = %v, want the positional %q verbatim", params["pane_id"], tc.id)
			}
			if params["direction"] != "right" {
				t.Errorf("params.direction = %v, want \"right\" from --direction", params["direction"])
			}

			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not one JSON object: %v (%q)", err, stdout.String())
			}
			pane, _ := got["pane"].(map[string]any)
			if pane == nil || pane["workspace_id"] != strings.SplitN(tc.wantResolved, ":", 2)[0] {
				t.Errorf("printed pane = %v, want the new pane beside %s, the pane %s resolves to", got["pane"], tc.wantResolved, tc.id)
			}
		})
	}

	t.Run("an identifier the daemon does not know exits 1", func(t *testing.T) {
		t.Parallel()
		d := serveRecordingDaemon(t, map[string]fakeReply{"pane.split": split})

		var stdout, stderr bytes.Buffer
		code := run([]string{
			"pane", "split", "w9:p9", "--direction", "right", "--json",
			"--socket", d.socket, "--no-autostart",
		}, &stdout, &stderr)
		if code != exitFailure {
			t.Errorf("exit = %d, want %d: the daemon answered NOT_FOUND (REQ-CLI-004); stderr: %s", code, exitFailure, stderr.String())
		}
		if len(d.callsTo("pane.split")) != 1 {
			t.Errorf("pane.split was called %d times, want 1: an unknown id is the daemon's to reject, not the CLI's", len(d.callsTo("pane.split")))
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q; a failed --json call must print nothing parsable", stdout.String())
		}
	})

	t.Run("no daemon exits 69", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer
		code := run([]string{
			"pane", "split", "w1:p1", "--direction", "right", "--json", "--no-autostart",
			"--socket", filepath.Join(t.TempDir(), "umbral.sock"),
		}, &stdout, &stderr)
		if code != exitUnavailable {
			t.Errorf("exit = %d, want %d (EX_UNAVAILABLE); stderr: %s", code, exitUnavailable, stderr.String())
		}
	})
}

// TestUnknownSubcommandExitsOne_REQ_CLI_005 covers the other side of "runs with one of the
// subcommands the daemon serves": a subcommand that is not one of them. REQ-CLI-004 gives it
// exit 1 ("the request was rejected"), the delta's Verification asks for usage and for the
// daemon never to be reached — a typo must not open a connection, let alone send a call the
// daemon has to reject as METHOD_NOT_FOUND.
//
// The fake daemon is live and reachable, so "no request arrived" is a statement about the
// CLI rather than about a missing socket. And the family itself must be recognised: today
// `umb workspace` is an unknown *command*, which also exits 1, and the first assertion keeps
// that from passing for the wrong reason.
func TestUnknownSubcommandExitsOne_REQ_CLI_005(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		family, typo, valid string
	}{
		{"workspace", "creat", "create"},
		{"tab", "lst", "list"},
		{"pane", "spilt", "split"},
		{"layout", "exprot", "export"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			t.Parallel()
			d := serveRecordingDaemon(t, nil)

			var stdout, stderr bytes.Buffer
			code := run([]string{
				tc.family, tc.typo, "w1",
				"--socket", d.socket, "--no-autostart",
			}, &stdout, &stderr)

			errText := stderr.String()
			if strings.Contains(errText, fmt.Sprintf("unknown command %q", tc.family)) {
				t.Fatalf("`umb %s` is itself an unknown command (stderr: %q); the %s family does not exist yet", tc.family, errText, tc.family)
			}
			if code != exitFailure {
				t.Errorf("exit = %d, want %d: a misspelled subcommand is a rejected request (REQ-CLI-004)", code, exitFailure)
			}
			if !strings.Contains(errText, tc.typo) {
				t.Errorf("stderr = %q does not name %q; the user cannot see what was wrong", errText, tc.typo)
			}
			if !strings.Contains(errText, tc.valid) {
				t.Errorf("stderr = %q does not list %q; a misspelling gets the family's usage", errText, tc.valid)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: usage for an error goes to stderr", stdout.String())
			}
			if calls := d.calls(); len(calls) != 0 {
				t.Errorf("the daemon received %v, want nothing: a typo must not reach it", methodsOf(calls))
			}
		})
	}
}

// requireExit fails the test when the command did not exit with want, quoting stderr, which
// is where the CLI says why.
func requireExit(t *testing.T, got, want int, stderr *bytes.Buffer) {
	t.Helper()
	if got != want {
		t.Fatalf("exit = %d, want %d; stderr: %s", got, want, stderr.String())
	}
}

// assertCarries checks that every key the daemon sent reaches the printed object with the
// same value. Extra keys are allowed: the CLI may decode into the API types and print fields
// the fake left out, which is not a loss.
func assertCarries(t *testing.T, name string, got, sent any) {
	t.Helper()
	// Normalise the sent value through JSON so numbers compare as float64 on both sides.
	raw, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	obj, ok := got.(map[string]any)
	if !ok {
		t.Errorf("printed %s = %v, want the object the daemon sent", name, got)
		return
	}
	for k, v := range want {
		gv, present := obj[k]
		if !present {
			t.Errorf("printed %s has no %q; a script reading it gets null for a value the daemon sent", name, k)
			continue
		}
		if !reflect.DeepEqual(gv, v) {
			t.Errorf("printed %s.%s = %v, want %v as the daemon sent it", name, k, gv, v)
		}
	}
}

// fakeReply answers one method: a result, or a non-empty domain code for an error.
type fakeReply func(params map[string]any) (result any, domainCode string)

// rpcCall is one request as the fake daemon received it.
type rpcCall struct {
	Method string
	Params map[string]any
}

// recordingDaemon is a fake umbrald that records every request, the handshake included, so a
// test can assert on the sequence of calls and not only on one method's params.
type recordingDaemon struct {
	socket string

	mu  sync.Mutex
	log []rpcCall
}

func (d *recordingDaemon) record(c rpcCall) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, c)
}

func (d *recordingDaemon) calls() []rpcCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]rpcCall(nil), d.log...)
}

func (d *recordingDaemon) callsExceptHello() []rpcCall {
	var out []rpcCall
	for _, c := range d.calls() {
		if c.Method != "system.hello" {
			out = append(out, c)
		}
	}
	return out
}

func (d *recordingDaemon) callsTo(method string) []rpcCall {
	var out []rpcCall
	for _, c := range d.calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func methodsOf(calls []rpcCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return out
}

// domainErrors maps the domain codes these tests use to their JSON-RPC codes (API Spec §3).
var domainErrors = map[string]int{
	"METHOD_NOT_FOUND": -32601,
	"VALIDATION_ERROR": -32602,
	"NOT_FOUND":        -32002,
}

// serveRecordingDaemon starts a socket that answers the handshake and the given methods and
// records every request. A method with no reply answers METHOD_NOT_FOUND, as a real daemon
// would for a name it does not know.
func serveRecordingDaemon(t *testing.T, replies map[string]fakeReply) *recordingDaemon {
	t.Helper()

	dir := t.TempDir()
	d := &recordingDaemon{socket: filepath.Join(dir, "umbral.sock")}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("write the token: %v", err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", d.socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", d.socket, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(conn, replies)
		}
	}()
	return d
}

func (d *recordingDaemon) serve(conn net.Conn, replies map[string]fakeReply) {
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
		d.record(rpcCall{Method: req.Method, Params: req.Params})

		if req.Method == "system.hello" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"daemon_version": "0.0.0-fake", "protocol_version": 1,
				"capabilities":  []string{"sessions", "blocks", "workspaces", "layouts"},
				"connection_id": "con_fake",
			}})
			continue
		}

		code := "METHOD_NOT_FOUND"
		var result any
		if reply, ok := replies[req.Method]; ok {
			result, code = reply(req.Params)
		}
		if code != "" {
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{
					"code": domainErrors[code], "message": strings.ToLower(code),
					"data": map[string]any{"domain_code": code},
				},
			})
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}
