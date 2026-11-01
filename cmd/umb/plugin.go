package main

import (
	"context"
	"flag"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	pluginInspect = "inspect"
	pluginInstall = "install"
	pluginList    = "list"
	pluginShow    = "show"
	pluginRemove  = "remove"
	pluginRun     = "run"
)

type pluginCommand struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Argv        []string `json:"argv"`
}

type pluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Commands    []pluginCommand `json:"commands"`
	MCPServers  []struct {
		Name      string            `json:"name"`
		Transport string            `json:"transport"`
		Command   *string           `json:"command"`
		Args      []string          `json:"args"`
		URL       *string           `json:"url"`
		EnvRefs   map[string]string `json:"env_refs"`
	} `json:"mcp_servers"`
	Skills []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"skills"`
	Detection []string `json:"detection"`
	Keys      []struct {
		Key     string `json:"key"`
		Command string `json:"command"`
	} `json:"keys"`
}

type pluginInfo struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Manifest    pluginManifest `json:"manifest"`
	Source      string         `json:"source"`
	SHA256      string         `json:"sha256"`
	SizeBytes   int64          `json:"size_bytes"`
	InstalledAt int64          `json:"installed_at"`
}

type pluginInspection struct {
	Manifest pluginManifest `json:"manifest"`
	Files    []struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"files"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// cmdPlugin is `umb plugin` (REQ-PLG-001, API Spec §5.45). install inspects first, shows the
// manifest — every command, server, skill, detection manifest and key — with its digest, and
// installs that digest only once the user confirmed it, as `umb skill install` does.
func cmdPlugin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr *printer) int {
	const usageText = "umb plugin: inspect PATH | install PATH [--yes] | list | show NAME | remove NAME | run PLUGIN COMMAND [--pane ID]"
	if len(args) == 0 {
		stderr.println(usageText)
		return exitFailure
	}
	name := args[0]
	fs := flag.NewFlagSet("umb plugin "+name, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	yes := fs.Bool("yes", false, "install without asking")
	pane := fs.String("pane", "", "the pane to split for run (default $UMBRAL_PANE_ID)")
	pos, more, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	pos = append(pos, more...)

	var method string
	var params map[string]any
	switch {
	case (name == pluginInspect || name == pluginInstall) && len(pos) == 1:
		method, params = "plugin.inspect", map[string]any{paramPath: sourcePath(pos[0])}
	case name == pluginList && len(pos) == 0:
		method, params = "plugin.list", map[string]any{}
	case name == pluginShow && len(pos) == 1:
		method, params = "plugin.get", map[string]any{paramName: pos[0]}
	case name == pluginRemove && len(pos) == 1:
		method, params = "plugin.remove", map[string]any{paramName: pos[0]}
	case name == pluginRun && len(pos) == 2:
		target := *pane
		if target == "" {
			target = os.Getenv("UMBRAL_PANE_ID")
		}
		if target == "" {
			stderr.println("umb plugin run: no pane: pass --pane, or run it from a pane, which sets $UMBRAL_PANE_ID")
			return exitFailure
		}
		method, params = "plugin.run", map[string]any{"plugin": pos[0], "command": pos[1], "pane_id": target}
	default:
		stderr.println(usageText)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	// Each call has its own deadline: install's starts after the confirmation.
	call := func(method string, params, out any) error {
		callCtx, cancel := context.WithTimeout(ctx, skillCallTimeout)
		defer cancel()
		return c.Call(callCtx, method, params, out)
	}
	prefix := "umb plugin " + name + ": "

	switch name {
	case pluginInspect, pluginInstall:
		var insp pluginInspection
		if err := call(method, params, &insp); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if name == pluginInspect && f.asJSON {
			return printJSON(stdout, stderr, insp)
		}
		// With --json, stdout carries the installed Plugin alone, so the manifest goes to stderr.
		shown := stdout
		if f.asJSON {
			shown = stderr
		}
		printPluginInspection(shown, insp)
		if name == pluginInspect {
			return exitOK
		}
		if !*yes && !confirmPlugin(stdin, stderr, insp) {
			return exitFailure
		}
		var p pluginInfo
		if err := call("plugin.install", map[string]any{paramPath: params[paramPath], "expected_sha256": insp.SHA256}, &p); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, p)
		}
		stdout.printf("installed %s %s\n", printable(p.Name), printable(p.SHA256))
		if len(p.Manifest.MCPServers) > 0 {
			stdout.println("its MCP servers are untrusted; their results ask before anything is run with them")
		}
		if len(p.Manifest.Detection) > 0 {
			stdout.println("its detection manifests apply from the daemon's next start")
		}
	case pluginList:
		var out struct {
			Items []pluginInfo `json:"items"`
		}
		if err := call(method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		if len(out.Items) == 0 {
			stdout.println("no plugins installed")
		}
		for _, p := range out.Items {
			stdout.printf("%s  %s  %s\n", printable(p.Name), printable(p.Manifest.Version), printable(firstLine(p.Manifest.Description, 72)))
		}
	case pluginShow:
		var p pluginInfo
		if err := call(method, params, &p); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, p)
		}
		printPluginManifest(stdout, p.Manifest)
		stdout.printf("sha256: %s\nsize: %d bytes\nsource: %s\ninstalled: %s\n", printable(p.SHA256), p.SizeBytes,
			printable(p.Source), time.UnixMilli(p.InstalledAt).UTC().Format(time.RFC3339))
	case pluginRemove:
		var out map[string]any
		if err := call(method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		stdout.printf("removed %s\n", printable(pos[0]))
	case pluginRun:
		var out struct {
			Pane struct {
				ID string `json:"id"`
			} `json:"pane"`
		}
		if err := call(method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		stdout.printf("%s\n", printable(out.Pane.ID))
	}
	return exitOK
}

// confirmPlugin is REQ-PLG-001's confirmation: a "y" typed on a terminal, or nothing.
func confirmPlugin(stdin io.Reader, stderr *printer, insp pluginInspection) bool {
	if !stdinIsTerminal(stdin) {
		stderr.println("umb plugin install: no terminal to confirm on; read the manifest above, then run again with --yes")
		return false
	}
	stderr.printf("Install %s with digest %s? [y/N] ", printable(insp.Manifest.Name), printable(insp.SHA256))
	if answeredYes(stdin, stderr) {
		return true
	}
	stderr.println("umb plugin install: not installed")
	return false
}

func printPluginInspection(p *printer, insp pluginInspection) {
	printPluginManifest(p, insp.Manifest)
	p.println("files:")
	for _, f := range insp.Files {
		p.printf("  %8d  %s\n", f.SizeBytes, printable(f.Path))
	}
	p.printf("size: %d bytes in %d files\nsha256: %s\n", insp.SizeBytes, len(insp.Files), printable(insp.SHA256))
}

// printPluginManifest shows everything the plugin would add, escaped: it comes from a bundle the
// user has not trusted yet, and this is what they confirm.
func printPluginManifest(p *printer, m pluginManifest) {
	p.printf("name: %s\nversion: %s\ndescription: %s\n", printable(m.Name), printable(m.Version), printable(m.Description))
	for _, c := range m.Commands {
		words := make([]string, 0, len(c.Argv))
		for _, w := range c.Argv {
			words = append(words, printable(w))
		}
		p.printf("command: %s  %s\n", printable(c.Name), strings.Join(words, " "))
	}
	for _, s := range m.MCPServers {
		target := ""
		switch {
		case s.Command != nil:
			words := make([]string, 0, 1+len(s.Args))
			words = append(words, printable(*s.Command))
			for _, a := range s.Args {
				words = append(words, printable(a))
			}
			target = strings.Join(words, " ")
		case s.URL != nil:
			target = printable(*s.URL)
		}
		p.printf("mcp server: %s  %s  %s  (untrusted)\n", printable(s.Name), printable(s.Transport), target)
		// The secrets a stdio server is started with as soon as it is installed: the user
		// confirms these too, so none can be asked for out of sight.
		for _, name := range slices.Sorted(maps.Keys(s.EnvRefs)) {
			p.printf("  env: %s <- %s\n", printable(name), printable(s.EnvRefs[name]))
		}
	}
	for _, s := range m.Skills {
		p.printf("skill: %s  %s\n", printable(s.Name), printable(s.Path))
	}
	for _, d := range m.Detection {
		p.printf("detection: %s\n", printable(d))
	}
	for _, k := range m.Keys {
		p.printf("key: %s  %s\n", printable(k.Key), printable(k.Command))
	}
}
