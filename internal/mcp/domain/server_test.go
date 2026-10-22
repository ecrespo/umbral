package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestToolNamesArePrefixed_REQ_MCP_001: a server's tool reaches the model as
// mcp_<server>_<tool>, folded into the names model APIs accept; a name that cannot fit is
// refused rather than truncated into a collision.
func TestToolNamesArePrefixed_REQ_MCP_001(t *testing.T) {
	for _, c := range []struct {
		server, tool, want string
		ok                 bool
	}{
		{"gitlab", "get_issue", "mcp_gitlab_get_issue", true},
		{"files", "read.File", "mcp_files_read_file", true},
		{"gh", "list-PRs", "mcp_gh_list_prs", true},
		{"gh", "", "", false},
		{"gh", strings.Repeat("x", 60), "", false},
	} {
		got, ok := ToolName(c.server, c.tool)
		if got != c.want || ok != c.ok {
			t.Errorf("ToolName(%q, %q) = %q, %v; want %q, %v", c.server, c.tool, got, ok, c.want, c.ok)
		}
	}
}

// TestAddParamsValidate_REQ_MCP_001: what mcp.server.add accepts. A plaintext env value is
// CONFIG_INVALID (REQ-SEC-004); the rest of a malformed request is VALIDATION_ERROR; the
// default trust is untrusted (REQ-MCP-004).
func TestAddParamsValidate_REQ_MCP_001(t *testing.T) {
	ok := AddParams{Name: "files", Transport: TransportStdio, Command: "mcp-files", EnvRefs: map[string]string{"TOKEN": "keyring:umbral/files"}}
	got, err := ok.Validate()
	if err != nil || got.Trust != TrustUntrusted {
		t.Fatalf("a valid stdio server: %+v %v", got, err)
	}
	if _, err := (AddParams{Name: "gh", Transport: TransportHTTP, URL: "https://example.com/mcp"}).Validate(); err != nil {
		t.Fatalf("a valid http server: %v", err)
	}
	for name, c := range map[string]struct {
		p    AddParams
		want error
	}{
		"plaintext env":      {AddParams{Name: "a", Transport: TransportStdio, Command: "x", EnvRefs: map[string]string{"TOKEN": "hunter2"}}, ErrConfigInvalid},
		"empty keyring path": {AddParams{Name: "a", Transport: TransportStdio, Command: "x", EnvRefs: map[string]string{"TOKEN": "keyring:"}}, ErrConfigInvalid},
		"bad env name":       {AddParams{Name: "a", Transport: TransportStdio, Command: "x", EnvRefs: map[string]string{"to ken": "env:T"}}, ErrValidation},
		"no name":            {AddParams{Transport: TransportStdio, Command: "x"}, ErrValidation},
		"upper-case name":    {AddParams{Name: "GitLab", Transport: TransportStdio, Command: "x"}, ErrValidation},
		"long name":          {AddParams{Name: strings.Repeat("a", 33), Transport: TransportStdio, Command: "x"}, ErrValidation},
		// `_` and `-` would let one server's prefix cover another's: a rule mcp_github_*
		// would also match server github_enterprise, and a-b and a_b would fold together.
		"underscore name": {AddParams{Name: "github_enterprise", Transport: TransportStdio, Command: "x"}, ErrValidation},
		"hyphen name":     {AddParams{Name: "my-files", Transport: TransportStdio, Command: "x"}, ErrValidation},
		// A remote server takes no credential in F1; one in its URL would be stored in clear.
		"userinfo in url":     {AddParams{Name: "a", Transport: TransportHTTP, URL: "https://u:ghp_x@h/mcp"}, ErrConfigInvalid},
		"unknown transport":   {AddParams{Name: "a", Transport: "sse", Command: "x"}, ErrValidation},
		"stdio without cmd":   {AddParams{Name: "a", Transport: TransportStdio}, ErrValidation},
		"stdio with url":      {AddParams{Name: "a", Transport: TransportStdio, Command: "x", URL: "http://h"}, ErrValidation},
		"http without url":    {AddParams{Name: "a", Transport: TransportHTTP}, ErrValidation},
		"http, not a url":     {AddParams{Name: "a", Transport: TransportHTTP, URL: "ftp://h/x"}, ErrValidation},
		"http with a command": {AddParams{Name: "a", Transport: TransportHTTP, URL: "http://h", Command: "x"}, ErrValidation},
		"http with env":       {AddParams{Name: "a", Transport: TransportHTTP, URL: "http://h", EnvRefs: map[string]string{"T": "env:T"}}, ErrValidation},
		"unknown trust":       {AddParams{Name: "a", Transport: TransportStdio, Command: "x", Trust: "maybe"}, ErrValidation},
	} {
		if _, err := c.p.Validate(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// TestBackoffDoubles_REQ_MCP_003: reconnection waits 1, 2, 4, 8 and 16 s, five attempts and
// no more.
func TestBackoffDoubles_REQ_MCP_003(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if MaxReconnects != len(want) {
		t.Fatalf("MaxReconnects = %d, want %d", MaxReconnects, len(want))
	}
	for i, w := range want {
		if got := Backoff(time.Second, i); got != w {
			t.Errorf("Backoff(1s, %d) = %v, want %v", i, got, w)
		}
	}
}

// TestRefsParse_REQ_MCP_001: the two reference forms, and nothing else.
func TestRefsParse_REQ_MCP_001(t *testing.T) {
	if r, err := ParseRef("keyring:umbral/gh"); err != nil || r.Kind != RefKeyring || r.Name != "umbral/gh" {
		t.Fatalf("keyring: %+v %v", r, err)
	}
	if r, err := ParseRef("env:GH_TOKEN"); err != nil || r.Kind != RefEnv || r.Name != "GH_TOKEN" {
		t.Fatalf("env: %+v %v", r, err)
	}
	for _, bad := range []string{"", "ghp_abc", "env:", "env:1X", "keyring:"} {
		if _, err := ParseRef(bad); !errors.Is(err, ErrConfigInvalid) {
			t.Errorf("ParseRef(%q) = %v, want CONFIG_INVALID", bad, err)
		}
	}
}

// TestRemote_REQ_MCP_001: which servers are off this machine (Art. 4).
func TestRemote_REQ_MCP_001(t *testing.T) {
	for u, want := range map[string]bool{
		"http://127.0.0.1:1/x": false, "http://localhost:8/x": false, "http://[::1]:8/x": false,
		"https://mcp.example.com/x": true, "http://10.0.0.5/x": true, "http://192.168.1.2/x": true,
	} {
		if got := Remote(Server{Transport: TransportHTTP, URL: u}); got != want {
			t.Errorf("Remote(%q) = %v, want %v", u, got, want)
		}
	}
	if Remote(Server{Transport: TransportStdio, Command: "x"}) {
		t.Error("a stdio server is not remote")
	}
}
