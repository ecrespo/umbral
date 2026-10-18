package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func mcpServerResult(name, transport, state string, lastError any) map[string]any {
	return map[string]any{
		"id": "mcp_1", "name": name, "transport": transport, "trust": "untrusted", "state": state,
		"tools": []string{"search"}, "last_error": lastError,
	}
}

// TestMcpCommandsMirrorTheMethods_REQ_CLI_008: `umb mcp add|list|remove` call the
// mcp.server.* method of the same name with API §5.27's params and print its result, JSON
// with --json, exiting as REQ-CLI-004 says. A server added from the CLI is always untrusted;
// `keyring:` and `env:` references pass through as env_refs, and a plaintext value is refused
// before anything is sent.
func TestMcpCommandsMirrorTheMethods_REQ_CLI_008(t *testing.T) {
	t.Parallel()
	seen := make(chan map[string]any, 16)
	socket := serveFakeDaemonWithParams(t, map[string]any{
		"mcp.server.add": mcpServerResult("gitlab", "stdio", "connecting", nil),
		"mcp.server.list": map[string]any{"items": []any{
			mcpServerResult("gitlab", "stdio", "connected", nil),
			mcpServerResult("jira", "http", "unavailable", "connection refused"),
		}},
		"mcp.server.remove": fakeError{
			"code": -32004, "message": "mcp server ghost is not configured",
			"data": map[string]any{"domain_code": "NOT_FOUND"},
		},
	}, seen)
	flags := []string{"--socket", socket, "--no-autostart"}
	call := func(args ...string) (int, string, string, map[string]any) {
		t.Helper()
		for len(seen) > 0 {
			<-seen
		}
		var stdout, stderr bytes.Buffer
		// The flags go before `--`, so a command after it keeps its own.
		full := []string{"mcp"}
		cut := len(args)
		for i, a := range args {
			if a == "--" {
				cut = i
				break
			}
		}
		full = append(append(append(full, args[:cut]...), flags...), args[cut:]...)
		code := run(full, &stdout, &stderr)
		var params map[string]any
		if len(seen) > 0 {
			params = <-seen
		}
		return code, stdout.String(), stderr.String(), params
	}

	code, out, errOut, params := call("add", "gitlab", "--stdio", "--env", "GITLAB_TOKEN=keyring:umbral/gitlab", "--env=HOME_DIR=env:HOME", "--", "npx", "-y", "@gitlab/mcp", "--env=prod")
	want := map[string]any{
		"name": "gitlab", "transport": "stdio", "command": "npx", "args": []any{"-y", "@gitlab/mcp", "--env=prod"},
		"env_refs": map[string]any{"GITLAB_TOKEN": "keyring:umbral/gitlab", "HOME_DIR": "env:HOME"}, "trust": "untrusted",
	}
	if code != exitOK || !reflect.DeepEqual(params, want) || !strings.Contains(out, "gitlab") {
		t.Fatalf("add --stdio: exit %d, params %v, stdout %q, stderr %q", code, params, out, errOut)
	}
	code, _, errOut, params = call("add", "jira", "--http", "https://jira.example/mcp")
	want = map[string]any{"name": "jira", "transport": "http", "url": "https://jira.example/mcp", "trust": "untrusted"}
	if code != exitOK || !reflect.DeepEqual(params, want) {
		t.Fatalf("add --http: exit %d, params %v, stderr %q", code, params, errOut)
	}
	if code, out, _, _ := call("add", "jira", "--http", "https://jira.example/mcp", "--json"); code != exitOK || !strings.HasPrefix(strings.TrimSpace(out), `{"id":"mcp_1"`) {
		t.Errorf("add --json: exit %d, %q", code, out)
	}

	// Refused before anything is sent: a plaintext value, a malformed --env, a trust flag,
	// no transport, both transports, a stdio server with no command, an http server with a
	// command or env_refs (§5.27 takes neither), extra arguments.
	for _, bad := range [][]string{
		{"add", "gitlab", "--stdio", "--env", "GITLAB_TOKEN=plaintext-typed-in-clear", "--", "npx"},
		{"add", "gitlab", "--stdio", "--env", "GITLAB_TOKEN", "--", "npx"},
		{"add", "gitlab", "--stdio", "--env=GITLAB_TOKEN=plaintext-typed-in-clear", "--", "npx"},
		{"add", "jira", "--http", "https://x", "--env"},
		{"add", "gitlab", "--stdio", "--env", "GITLAB_TOKEN=keyring:", "--", "npx"},
		{"add", "gitlab", "--stdio", "--trusted", "--", "npx"},
		{"add", "gitlab", "--", "npx"},
		{"add", "gitlab", "--stdio", "--http", "https://x", "--", "npx"},
		{"add", "gitlab", "--stdio"},
		{"add", "jira", "--http", "https://x", "--", "npx"},
		{"add", "jira", "--http", "https://x", "--env", "T=env:T"},
		{"add"},
		{"remove"},
		{"list", "extra"},
		{"list", "--env", "A=keyring:a"},
		{"remove", "gitlab", "--env", "A=keyring:a"},
		{"frobnicate"},
	} {
		code, out, errOut, params := call(bad...)
		if code != exitFailure || params != nil {
			t.Errorf("umb mcp %v: exit %d, sent %v", bad, code, params)
		}
		if strings.Contains(out+errOut, "plaintext-typed-in-clear") {
			t.Errorf("umb mcp %v echoed the secret: %q %q", bad, out, errOut)
		}
	}

	code, out, _, _ = call("list")
	if code != exitOK || !strings.Contains(out, "gitlab  stdio  untrusted  connected") || !strings.Contains(out, "jira  http  untrusted  unavailable  connection refused") {
		t.Errorf("list: exit %d, %q", code, out)
	}
	if code, out, _, _ := call("list", "--json"); code != exitOK || !strings.HasPrefix(strings.TrimSpace(out), `{"items":[`) {
		t.Errorf("list --json: exit %d, %q", code, out)
	}
	code, out, errOut, params = call("remove", "ghost")
	if code != exitFailure || params["name"] != "ghost" || out != "" || !strings.Contains(errOut, "not configured") {
		t.Errorf("remove of an unknown server: exit %d, params %v, stdout %q, stderr %q", code, params, out, errOut)
	}
}

// TestEnvIsTakenOutBeforeTheFlags_REQ_CLI_008: --env is read before the flag parser, which
// would quote a refused value in its error. A trailing --env with no value is an error, not
// an absent reference, and nothing after `--` is read, since it is the server's command line.
func TestEnvIsTakenOutBeforeTheFlags_REQ_CLI_008(t *testing.T) {
	t.Parallel()
	if _, _, err := takeEnv([]string{"--http", "https://x", "--env"}); err == nil {
		t.Error("a trailing --env was accepted")
	}
	rest, refs, err := takeEnv([]string{"-env", "A=env:A", "--stdio", "--", "npx", "--env", "B=plain"})
	if err != nil || !reflect.DeepEqual(rest, []string{"--stdio", "--", "npx", "--env", "B=plain"}) || !reflect.DeepEqual(refs, envRefs{"A": "env:A"}) {
		t.Errorf("takeEnv = %v, %v, %v", rest, refs, err)
	}
}
