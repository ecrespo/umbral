package api

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	plgdomain "github.com/ecrespo/umbral/internal/plugins/domain"
	plgports "github.com/ecrespo/umbral/internal/plugins/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// fakePlugins answers one fixed plugin and the store's sentinels: what the test sees is api's
// half.
type fakePlugins struct{ installed []string }

var fakePlugin = plgdomain.Plugin{
	ID: "plg_1", Source: "/src/acme", SHA256: "ab", SizeBytes: 9, InstalledAt: 7,
	Manifest: plgdomain.Manifest{
		Name: "acme", Version: "1.0", Description: "Acme tools",
		Commands: []plgdomain.Command{{Name: "deploy", Argv: []string{"make", "deploy"}}},
		Servers:  []plgdomain.Server{{Name: "acme", Transport: "stdio", Command: "acme-mcp"}},
		Skills:   []plgdomain.Skill{{Path: "skills/review", Name: "acme-review"}},
		Keys:     []plgdomain.Key{{Key: "ctrl+alt+d", Command: "deploy"}},
	},
}

func (f *fakePlugins) Inspect(_ context.Context, path string) (plgdomain.Inspection, error) {
	if !strings.HasPrefix(path, "/") {
		return plgdomain.Inspection{}, fmt.Errorf("%w: %q must be an absolute path", plgdomain.ErrValidation, path)
	}
	return plgdomain.Inspection{Manifest: fakePlugin.Manifest, Files: []plgdomain.File{{Path: plgdomain.ManifestFile, Size: 9}}, SizeBytes: 9, SHA256: "ab"}, nil
}

func (f *fakePlugins) Install(_ context.Context, path, expected string) (plgdomain.Plugin, error) {
	if expected != "ab" {
		return plgdomain.Plugin{}, fmt.Errorf("%w: the bundle changed", plgdomain.ErrConflict)
	}
	f.installed = append(f.installed, path)
	return fakePlugin, nil
}

func (f *fakePlugins) List(context.Context) ([]plgdomain.Plugin, error) {
	return []plgdomain.Plugin{fakePlugin}, nil
}

func (f *fakePlugins) Get(_ context.Context, name string) (plgdomain.Plugin, error) {
	if name != "acme" {
		return plgdomain.Plugin{}, plgdomain.ErrNotFound
	}
	return fakePlugin, nil
}

func (f *fakePlugins) Remove(context.Context, string) error { return nil }

// TestPluginsOverTheWire_REQ_PLG_001: `umb` may call plugin.*, `plugins` is advertised, the
// results have API §5.45's shapes, a changed digest is CONFLICT, plugin.run splits the pane
// with the command's argv as pane.split would, and plugin.changed reaches clients.
func TestPluginsOverTheWire_REQ_PLG_001(t *testing.T) {
	t.Parallel()
	svc := &fakePlugins{}
	tree := &fakeTree{pane: wsdomain.Pane{ID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1"}}
	b := bus.New()
	s := testServerWithConfig(t, Config{Plugins: svc, Workspaces: tree, Bus: b})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Notify(ctx)
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("hello: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), "plugins") {
		t.Errorf("plugins is not advertised: %v", capabilitiesOf(t, hello.Result))
	}
	manifestJSON := `{"name":"acme","version":"1.0","description":"Acme tools",` +
		`"commands":[{"name":"deploy","description":"","argv":["make","deploy"]}],` +
		`"mcp_servers":[{"name":"acme","transport":"stdio","command":"acme-mcp","args":[],"url":null,"env_refs":{}}],` +
		`"skills":[{"name":"acme-review","path":"skills/review"}],"detection":[],` +
		`"keys":[{"key":"ctrl+alt+d","command":"deploy"}]}`
	pluginJSON := `{"id":"plg_1","name":"acme","manifest":` + manifestJSON + `,"source":"/src/acme","sha256":"ab","size_bytes":9,"installed_at":7}`

	resp := c.call(2, "plugin.inspect", map[string]any{"path": "/src/acme"})
	if got := string(ordered[pluginInspection](t, resp.Result)); resp.Error != nil ||
		got != `{"manifest":`+manifestJSON+`,"files":[{"path":"umbral-plugin.toml","size_bytes":9}],"size_bytes":9,"sha256":"ab"}` {
		t.Errorf("inspect = %s %+v", got, resp.Error)
	}
	resp = c.call(3, "plugin.install", map[string]any{"path": "/src/acme", "expected_sha256": "ab"})
	if got := string(ordered[Plugin](t, resp.Result)); resp.Error != nil || got != pluginJSON || len(svc.installed) != 1 {
		t.Errorf("install = %s %+v", got, resp.Error)
	}
	resp = c.call(4, "plugin.list", nil)
	if got := string(ordered[pluginListResult](t, resp.Result)); resp.Error != nil || got != `{"items":[`+pluginJSON+`]}` {
		t.Errorf("list = %s %+v", got, resp.Error)
	}
	resp = c.call(5, "plugin.run", map[string]any{"plugin": "acme", "command": "deploy", "pane_id": "w1:p1"})
	if resp.Error != nil {
		t.Fatalf("run: %+v", resp.Error)
	}
	if got := tree.lastCall.splitParams; got.PaneID != "w1:p1" || !slices.Equal(got.Command, []string{"make", "deploy"}) || got.Direction != wsdomain.SplitRight {
		t.Errorf("plugin.run split %+v, want w1:p1 with make deploy", got)
	}
	if resp = c.call(6, "plugin.remove", map[string]any{"name": "acme"}); resp.Error != nil {
		t.Errorf("remove: %+v", resp.Error)
	}

	for name, tc := range map[string]struct {
		method string
		params map[string]any
		code   string
	}{
		"a relative path":    {"plugin.inspect", map[string]any{"path": "acme"}, domainValidationError},
		"a changed digest":   {"plugin.install", map[string]any{"path": "/src/x", "expected_sha256": "cd"}, domainConflict},
		"no digest":          {"plugin.install", map[string]any{"path": "/src/x"}, domainValidationError},
		"an unknown plugin":  {"plugin.get", map[string]any{"name": "nope"}, domainNotFound},
		"an unknown command": {"plugin.run", map[string]any{"plugin": "acme", "command": "nope", "pane_id": "w1:p1"}, domainNotFound},
		"no pane":            {"plugin.run", map[string]any{"plugin": "acme", "command": "deploy"}, domainValidationError},
	} {
		resp := c.call(8, tc.method, tc.params)
		if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != tc.code {
			t.Errorf("%s: %+v, want %s", name, resp.Error, tc.code)
		}
	}

	b.Publish(plgports.PluginChanged{Name: "acme", Change: plgdomain.ChangeRemoved})
	method, _, n := c.readNotification(t)
	if method != "plugin.changed" || string(n) != `{"name":"acme","change":"removed"}` {
		t.Fatalf("plugin.changed = %s %s", method, n)
	}
}

// TestPluginsAreNotImplementedWithoutTheStore: a daemon built without the plugin store answers
// plugin.* NOT_IMPLEMENTED and does not advertise `plugins`.
func TestPluginsAreNotImplementedWithoutTheStore(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if slices.Contains(capabilitiesOf(t, hello.Result), "plugins") {
		t.Error("plugins is advertised without the store")
	}
	resp := c.call(2, "plugin.list", nil)
	if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("plugin.list = %+v, want NOT_IMPLEMENTED", resp.Error)
	}
}
