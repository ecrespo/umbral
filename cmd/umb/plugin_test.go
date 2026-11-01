package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

const pluginDigest = "5e1d22aa"

func pluginManifestResult() map[string]any {
	return map[string]any{
		"name": "acme", "version": "1.2.0", "description": "Acme tools",
		"commands":    []map[string]any{{"name": "deploy", "description": "", "argv": []string{"make", "deploy"}}},
		"mcp_servers": []map[string]any{{"name": "acme", "transport": "stdio", "command": "acme-mcp", "args": []string{"--stdio"}, "url": nil, "env_refs": map[string]any{"ACME_TOKEN": "keyring:acme/token", "OPENAI_API_KEY": "env:OPENAI_API_KEY"}}},
		"skills":      []map[string]any{{"name": "acme-review", "path": "skills/review"}},
		"detection":   []string{"detect/acme.toml"},
		"keys":        []map[string]any{{"key": "ctrl+alt+d", "command": "deploy"}},
	}
}

func pluginDaemon(t *testing.T) (string, chan map[string]any) {
	t.Helper()
	seen := make(chan map[string]any, 16)
	socket := serveFakeDaemonWithParams(t, map[string]any{
		"plugin.inspect": map[string]any{
			"manifest": pluginManifestResult(), "size_bytes": 300, "sha256": pluginDigest,
			"files": []map[string]any{{"path": "umbral-plugin.toml", "size_bytes": 300}},
		},
		"plugin.install": map[string]any{
			"id": "plg_1", "name": "acme", "manifest": pluginManifestResult(), "source": "/src/acme",
			"sha256": pluginDigest, "size_bytes": 300, "installed_at": 1,
		},
		"plugin.run": map[string]any{"pane": map[string]any{"id": "w1:p3"}, "layout": map[string]any{}},
	}, seen)
	return socket, seen
}

// TestPluginInstallShowsEverythingAndNeedsAYes_REQ_PLG_001: `umb plugin install` shows every
// command, server — with every secret reference it would be started with — skill, detection
// manifest and key with the digest, and installs that digest only after a "y" on a terminal
// or with --yes.
func TestPluginInstallShowsEverythingAndNeedsAYes_REQ_PLG_001(t *testing.T) {
	socket, seen := pluginDaemon(t)
	src := t.TempDir()
	base := []string{"plugin", "install", src, "--socket", socket, "--no-autostart"}

	var stdout, stderr bytes.Buffer
	if code := run(base, &stdout, &stderr); code != exitFailure {
		t.Fatalf("no terminal, no --yes: exit %d", code)
	}
	for _, want := range []string{"acme", pluginDigest, "make deploy", "acme-mcp --stdio", "(untrusted)", "ACME_TOKEN <- keyring:acme/token", "OPENAI_API_KEY <- env:OPENAI_API_KEY", "acme-review", "detect/acme.toml", "ctrl+alt+d"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the manifest shown lacks %q: %q", want, stdout.String())
		}
	}
	for len(seen) > 0 {
		if p := <-seen; p["expected_sha256"] != nil {
			t.Fatalf("installed without a confirmation: %v", p)
		}
	}

	defer func(prev func(io.Reader) bool) { stdinIsTerminal = prev }(stdinIsTerminal)
	stdinIsTerminal = func(io.Reader) bool { return true }
	for answer, want := range map[string]int{"n\n": 0, "": 0, "y\n": 1} {
		stdout.Reset()
		code := runIO(base, strings.NewReader(answer), &stdout, &stderr)
		installs := 0
		for len(seen) > 0 {
			if p := <-seen; p["expected_sha256"] != nil {
				installs++
				if p["expected_sha256"] != pluginDigest || p["path"] != src {
					t.Errorf("installed %v, want the digest shown from %s", p, src)
				}
			}
		}
		if installs != want {
			t.Errorf("answer %q: %d installs (exit %d)", answer, installs, code)
		}
	}
}

// TestPluginRunDefaultsToThePaneItRunsIn_REQ_PLG_002: `umb plugin run` asks the daemon to split
// $UMBRAL_PANE_ID, or --pane, and refuses with neither.
func TestPluginRunDefaultsToThePaneItRunsIn_REQ_PLG_002(t *testing.T) {
	socket, seen := pluginDaemon(t)
	t.Setenv("UMBRAL_PANE_ID", "")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plugin", "run", "acme", "deploy", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitFailure {
		t.Fatalf("no pane: exit %d", code)
	}
	t.Setenv("UMBRAL_PANE_ID", "w1:p1")
	if code := run([]string{"plugin", "run", "acme", "deploy", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("run: exit %d: %s", code, stderr.String())
	}
	if p := <-seen; p["plugin"] != "acme" || p["command"] != "deploy" || p["pane_id"] != "w1:p1" {
		t.Errorf("plugin.run params = %v", p)
	}
	if code := run([]string{"plugin", "run", "acme", "deploy", "--pane", "w2:p4", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("run --pane: exit %d", code)
	}
	if p := <-seen; p["pane_id"] != "w2:p4" {
		t.Errorf("--pane sent %v", p)
	}
	if !strings.Contains(stdout.String(), "w1:p3") {
		t.Errorf("the new pane was not printed: %q", stdout.String())
	}
}
