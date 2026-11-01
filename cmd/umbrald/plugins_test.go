package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// TestAPluginInstalledByDigestAddsAServerAndACommand_REQ_PLG_001 is F2f's exit criterion
// against a real daemon, through the real `umb`: a plugin installed by its digest adds an MCP
// server, untrusted (REQ-PLG-002), and a skill; its command runs in a pane split from the one
// named; and removing it takes away what it added (delta `2026-10-plugins`).
func TestAPluginInstalledByDigestAddsAServerAndACommand_REQ_PLG_001(t *testing.T) {
	bin, umb := buildDaemon(t), buildUmb(t)
	_, daemonDir := isolatedRuntime(t)
	dir := socketDir(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))

	bundle := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(filepath.Join(bundle, "skills", "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bundle, "umbral-plugin.toml"), `name = "acme"
version = "1.0.0"
description = "Acme tools"

[[commands]]
name = "hello"
argv = ["echo", "acme hello"]

[[mcp_servers]]
name = "acme"
transport = "stdio"
command = "acme-mcp-not-installed"

[[skills]]
path = "skills/review"

[[keys]]
key = "ctrl+alt+h"
command = "hello"
`)
	writeFile(t, filepath.Join(bundle, "skills", "review", "SKILL.md"),
		"---\nname: acme-review\ndescription: Review the acme way\n---\nReview carefully.\n")

	socket := filepath.Join(daemonDir, "umbral.sock")
	runDaemon(t, bin, nil)
	_ = waitForSocket(t, socket).Close()

	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), umb, append(args, "--no-autostart")...).CombinedOutput()
		if err != nil {
			t.Fatalf("umb %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// Without a terminal or --yes nothing is installed.
	cmd := exec.CommandContext(t.Context(), umb, "plugin", "install", bundle, "--no-autostart")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("an unconfirmed install succeeded:\n%s", out)
	}
	if out := run("plugin", "list"); !strings.Contains(out, "no plugins installed") {
		t.Fatalf("an unconfirmed install left a plugin:\n%s", out)
	}

	inspected := run("plugin", "inspect", bundle)
	out := run("plugin", "install", bundle, "--yes")
	if !strings.Contains(out, "installed acme ") || !strings.Contains(inspected, "mcp server: acme") {
		t.Fatalf("install:\n%s\ninspect:\n%s", out, inspected)
	}
	digest := strings.Fields(out[strings.Index(out, "installed acme "):])[2]
	if !strings.Contains(inspected, "sha256: "+digest) {
		t.Errorf("the installed digest %s is not the one inspect showed:\n%s", digest, inspected)
	}

	ctx := t.Context()
	c, err := client.Connect(ctx, client.Options{SocketPath: socket, NoAutostart: true})
	if err != nil {
		t.Fatal(err)
	}
	s := client.NewStream(c)
	t.Cleanup(func() { _ = s.Close() })

	var servers struct {
		Items []struct{ Name, Trust string } `json:"items"`
	}
	if err := s.Call(ctx, "mcp.server.list", map[string]any{}, &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers.Items) != 1 || servers.Items[0].Name != "acme" || servers.Items[0].Trust != "untrusted" {
		t.Errorf("mcp.server.list = %+v, want acme untrusted", servers.Items)
	}
	if names := skillNames(t, s); !slices.Equal(names, []string{"acme-review"}) {
		t.Errorf("skills = %v, want acme-review", names)
	}

	var ws struct {
		RootPane struct {
			ID string `json:"id"`
		} `json:"root_pane"`
	}
	if err := s.Call(ctx, "workspace.create", map[string]any{"cwd": t.TempDir()}, &ws); err != nil {
		t.Fatal(err)
	}
	paneID := strings.TrimSpace(run("plugin", "run", "acme", "hello", "--pane", ws.RootPane.ID))
	var pane struct {
		Pane struct {
			Command []string `json:"command"`
		} `json:"pane"`
	}
	if err := s.Call(ctx, "pane.get", map[string]any{"pane_id": paneID}, &pane); err != nil {
		t.Fatalf("pane.get %q: %v", paneID, err)
	}
	if !slices.Equal(pane.Pane.Command, []string{"echo", "acme hello"}) {
		t.Errorf("the new pane runs %q, want the plugin's command", pane.Pane.Command)
	}

	run("plugin", "remove", "acme")
	if err := s.Call(ctx, "mcp.server.list", map[string]any{}, &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers.Items) != 0 {
		t.Errorf("a removed plugin's server stayed: %+v", servers.Items)
	}
	if names := skillNames(t, s); len(names) != 0 {
		t.Errorf("a removed plugin's skill stayed: %v", names)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "plugins"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 1 && entries[0].Name() == ".umbral-plugins" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugins/ after remove holds %v", entries)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func skillNames(t *testing.T, s *client.Stream) []string {
	t.Helper()
	var skills struct {
		Items []struct{ Name string } `json:"items"`
	}
	if err := s.Call(t.Context(), "skill.list", map[string]any{}, &skills); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(skills.Items))
	for _, sk := range skills.Items {
		names = append(names, sk.Name)
	}
	return names
}
