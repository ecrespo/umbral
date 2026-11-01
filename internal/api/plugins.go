package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	plgdomain "github.com/ecrespo/umbral/internal/plugins/domain"
	plgports "github.com/ecrespo/umbral/internal/plugins/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// capabilityPlugins is §2's reserved `plugins`: plugin.* (REQ-PLG-001, delta `2026-10-plugins`).
const capabilityPlugins = "plugins"

// PluginCommand is one of a plugin's commands (API §4 `Plugin`).
type PluginCommand struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Argv        []string `json:"argv"`
}

// PluginServer is an MCP server a plugin declares; it is added untrusted (REQ-PLG-002).
type PluginServer struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   *string           `json:"command"`
	Args      []string          `json:"args"`
	URL       *string           `json:"url"`
	EnvRefs   map[string]string `json:"env_refs"`
}

// PluginSkill is a skill a plugin carries.
type PluginSkill struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// PluginKey binds a key to one of the plugin's commands.
type PluginKey struct {
	Key     string `json:"key"`
	Command string `json:"command"`
}

// PluginManifest is a plugin's parsed manifest.
type PluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Commands    []PluginCommand `json:"commands"`
	MCPServers  []PluginServer  `json:"mcp_servers"`
	Skills      []PluginSkill   `json:"skills"`
	Detection   []string        `json:"detection"`
	Keys        []PluginKey     `json:"keys"`
}

// Plugin is API Spec §4's Plugin.
type Plugin struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Manifest    PluginManifest `json:"manifest"`
	Source      string         `json:"source"`
	SHA256      string         `json:"sha256"`
	SizeBytes   int64          `json:"size_bytes"`
	InstalledAt int64          `json:"installed_at"`
}

func toWireManifest(m plgdomain.Manifest) PluginManifest {
	out := PluginManifest{
		Name: m.Name, Version: m.Version, Description: m.Description,
		Commands: make([]PluginCommand, 0, len(m.Commands)), MCPServers: make([]PluginServer, 0, len(m.Servers)),
		Skills: make([]PluginSkill, 0, len(m.Skills)), Detection: make([]string, 0, len(m.Detection)),
		Keys: make([]PluginKey, 0, len(m.Keys)),
	}
	for _, c := range m.Commands {
		out.Commands = append(out.Commands, PluginCommand{Name: c.Name, Description: c.Description, Argv: c.Argv})
	}
	for _, s := range m.Servers {
		args, env := s.Args, s.EnvRefs
		if args == nil {
			args = []string{}
		}
		if env == nil {
			env = map[string]string{}
		}
		out.MCPServers = append(out.MCPServers, PluginServer{
			Name: s.Name, Transport: s.Transport, Command: emptyAsNull(s.Command), Args: args, URL: emptyAsNull(s.URL), EnvRefs: env,
		})
	}
	for _, s := range m.Skills {
		out.Skills = append(out.Skills, PluginSkill{Name: s.Name, Path: s.Path})
	}
	for _, d := range m.Detection {
		out.Detection = append(out.Detection, d.Path)
	}
	for _, k := range m.Keys {
		out.Keys = append(out.Keys, PluginKey{Key: k.Key, Command: k.Command})
	}
	return out
}

func toWirePlugin(p plgdomain.Plugin) Plugin {
	return Plugin{
		ID: p.ID, Name: p.Name(), Manifest: toWireManifest(p.Manifest), Source: p.Source,
		SHA256: p.SHA256, SizeBytes: p.SizeBytes, InstalledAt: p.InstalledAt,
	}
}

// pluginChangedPayload is `plugin.changed` (API §6).
type pluginChangedPayload struct {
	Name   string `json:"name"`
	Change string `json:"change"`
}

func hasPlugins(cfg Config) bool { return cfg.Plugins != nil }

func pluginStore(c *conn) (plgports.Plugins, error) {
	if c.server.cfg.Plugins == nil {
		return nil, fmt.Errorf("%w: the plugin store is not wired in", ErrNotImplemented)
	}
	return c.server.cfg.Plugins, nil
}

// pluginMethods is §5.45. Every client kind may call them, as skill.* (decision 10); the three
// that change state are refused to an agent's terminal, so a plugin's commands and servers
// reach the agent only as the user's (REQ-PLG-002).
func pluginMethods() map[string]method {
	return map[string]method{
		"plugin.inspect": {handle: handlePluginInspect, kinds: treeClients, params: skillPathParams{}, result: pluginInspection{}},
		"plugin.install": {handle: handlePluginInstall, userOnly: true, kinds: treeClients, params: pluginInstallParams{}, result: Plugin{}},
		"plugin.list":    {handle: handlePluginList, kinds: treeClients, params: emptyResult{}, result: pluginListResult{}},
		"plugin.get":     {handle: handlePluginGet, kinds: treeClients, params: skillNameParams{}, result: Plugin{}},
		"plugin.remove":  {handle: handlePluginRemove, userOnly: true, kinds: treeClients, params: skillNameParams{}, result: emptyResult{}},
		"plugin.run":     {handle: handlePluginRun, userOnly: true, kinds: treeClients, params: pluginRunParams{}, result: paneSplitResult{}},
	}
}

type pluginInspection struct {
	Manifest  PluginManifest `json:"manifest"`
	Files     []skillFile    `json:"files"`
	SizeBytes int64          `json:"size_bytes"`
	SHA256    string         `json:"sha256"`
}

func handlePluginInspect(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := pluginStore(c)
	if err != nil {
		return nil, err
	}
	var p skillPathParams
	if err := decode(raw, &p, "plugin.inspect"); err != nil {
		return nil, err
	}
	if err := requirePath(p.Path); err != nil {
		return nil, err
	}
	insp, err := svc.Inspect(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	out := pluginInspection{Manifest: toWireManifest(insp.Manifest), SizeBytes: insp.SizeBytes, SHA256: insp.SHA256, Files: make([]skillFile, 0, len(insp.Files))}
	for _, f := range insp.Files {
		out.Files = append(out.Files, skillFile{Path: f.Path, SizeBytes: f.Size})
	}
	return out, nil
}

type pluginInstallParams struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

func handlePluginInstall(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := pluginStore(c)
	if err != nil {
		return nil, err
	}
	var p pluginInstallParams
	if err := decode(raw, &p, "plugin.install"); err != nil {
		return nil, err
	}
	if err := requirePath(p.Path); err != nil {
		return nil, err
	}
	if p.ExpectedSHA256 == "" {
		return nil, ValidationError("expected_sha256 is required: install exactly what plugin.inspect showed",
			ErrorField{Field: fieldExpectedSHA256, Issue: requiredTag})
	}
	installed, err := svc.Install(ctx, p.Path, p.ExpectedSHA256)
	if err != nil {
		return nil, err
	}
	return toWirePlugin(installed), nil
}

type pluginListResult struct {
	Items []Plugin `json:"items"`
}

func handlePluginList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := pluginStore(c)
	if err != nil {
		return nil, err
	}
	if err := decode(raw, &emptyResult{}, "plugin.list"); err != nil {
		return nil, err
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := pluginListResult{Items: make([]Plugin, 0, len(list))}
	for _, p := range list {
		out.Items = append(out.Items, toWirePlugin(p))
	}
	return out, nil
}

func handlePluginGet(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := pluginStore(c)
	if err != nil {
		return nil, err
	}
	var p skillNameParams
	if err := decode(raw, &p, "plugin.get"); err != nil {
		return nil, err
	}
	if err := requireName(p.Name); err != nil {
		return nil, err
	}
	got, err := svc.Get(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	return toWirePlugin(got), nil
}

func handlePluginRemove(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := pluginStore(c)
	if err != nil {
		return nil, err
	}
	var p skillNameParams
	if err := decode(raw, &p, "plugin.remove"); err != nil {
		return nil, err
	}
	if err := requireName(p.Name); err != nil {
		return nil, err
	}
	if err := svc.Remove(ctx, p.Name); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

type pluginRunParams struct {
	Plugin  string `json:"plugin"`
	Command string `json:"command"`
	PaneID  string `json:"pane_id"`
}

// handlePluginRun splits the pane with the command's argv, as pane.split with a command does:
// the user launched it, and the daemon decides nothing for it (decision 6).
func handlePluginRun(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := pluginStore(c)
	if err != nil {
		return nil, err
	}
	tree, err := c.tree()
	if err != nil {
		return nil, err
	}
	var p pluginRunParams
	if err := decode(raw, &p, "plugin.run"); err != nil {
		return nil, err
	}
	for field, v := range map[string]string{"plugin": p.Plugin, "command": p.Command, "pane_id": p.PaneID} {
		if v == "" {
			return nil, ValidationError(field+" is required", ErrorField{Field: field, Issue: requiredTag})
		}
	}
	plugin, err := svc.Get(ctx, p.Plugin)
	if err != nil {
		return nil, err
	}
	cmd, ok := plugin.Command(p.Command)
	if !ok {
		return nil, fmt.Errorf("%w: plugin %s has no command %s", plgdomain.ErrNotFound, p.Plugin, p.Command)
	}
	pane, layout, err := tree.SplitPane(ctx, wsdomain.SplitParams{
		PaneID: p.PaneID, Direction: wsdomain.SplitRight, Command: cmd.Argv, Focus: true,
	})
	if err != nil {
		return nil, err
	}
	return paneSplitResult{Pane: toWirePane(pane), Layout: toWireLayout(layout)}, nil
}

// pluginsDomainError maps the plugin store's sentinels onto §3.
func pluginsDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, plgdomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, plgdomain.ErrConflict):
		return codeConflict, domainConflict, true
	case errors.Is(err, plgdomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	default:
		return 0, "", false
	}
}
