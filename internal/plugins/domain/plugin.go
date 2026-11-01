// Package domain is the domain layer of the plugins module: declarative plugins installed by
// digest, whose manifest contributes commands, MCP servers, skills, detection manifests and key
// bindings (REQ-PLG-001, REQ-PLG-002, delta `2026-10-plugins`). It imports nothing outside the
// standard library and other modules' domain (Art. 3).
package domain

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"

	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
)

// Errors the module returns; internal/api maps them onto API §3.
var (
	ErrValidation = errors.New("plugins: invalid plugin")
	ErrConflict   = errors.New("plugins: conflict")
	ErrNotFound   = errors.New("plugins: no such plugin")
)

// ManifestFile is the manifest's name at the bundle's root (decision 1).
const ManifestFile = "umbral-plugin.toml"

// Bounds of a manifest (decision 2).
const (
	MaxNameLen        = 32
	MaxVersionLen     = 64
	MaxDescriptionLen = 1024
	MaxCommands       = 32
	MaxArgv           = 64
	MaxServers        = 8
	MaxSkills         = 16
	MaxDetection      = 16
	MaxKeys           = 32
)

// The changes plugin.changed announces (decision 9).
const (
	ChangeInstalled = "installed"
	ChangeRemoved   = "removed"
)

// Command is a named argv the user launches in a new pane (decision 6).
type Command struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Argv        []string `json:"argv"`
}

// Server is an MCP server the plugin declares: mcp.server.add's fields without a trust, which
// is always untrusted (REQ-PLG-002).
type Server struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	URL       string            `json:"url,omitempty"`
	EnvRefs   map[string]string `json:"env_refs,omitempty"`
}

// AddParams is the server as mcp.server.add takes it: untrusted, whatever else is declared.
func (s Server) AddParams() mcpdomain.AddParams {
	return mcpdomain.AddParams{
		Name: s.Name, Transport: mcpdomain.Transport(s.Transport), Command: s.Command, Args: s.Args,
		URL: s.URL, EnvRefs: s.EnvRefs, Trust: mcpdomain.TrustUntrusted,
	}
}

// Skill is a skill bundle inside the plugin: Path is a directory of the bundle; Name, the name
// its SKILL.md gives, and SHA256, the skill store's digest of that directory, are filled in by
// the check. The digest is how a later remove tells the plugin's skill from one the user
// installed under the same name (decision 9).
type Skill struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Detection is an agent detection manifest file inside the plugin.
type Detection struct {
	Path string `json:"path"`
}

// Key binds a key to one of the plugin's commands (decision 8).
type Key struct {
	Key     string `json:"key"`
	Command string `json:"command"`
}

// Manifest is a parsed `umbral-plugin.toml`.
type Manifest struct {
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Description string      `json:"description"`
	Commands    []Command   `json:"commands"`
	Servers     []Server    `json:"mcp_servers"`
	Skills      []Skill     `json:"skills"`
	Detection   []Detection `json:"detection"`
	Keys        []Key       `json:"keys"`
}

// File is one file of a bundle, as inspect lists it.
type File struct {
	Path string
	Size int64
}

// Inspection is what plugin.inspect shows before anything is written.
type Inspection struct {
	Manifest  Manifest
	Files     []File
	SizeBytes int64
	SHA256    string
}

// Plugin is an installed plugin, API §4's `Plugin`.
type Plugin struct {
	ID       string
	Manifest Manifest
	// Source is the path it was installed from, for display only: the stored copy is used.
	Source      string
	SHA256      string
	SizeBytes   int64
	InstalledAt int64
}

// Name is the plugin's name.
func (p Plugin) Name() string { return p.Manifest.Name }

// Command returns the plugin's command by name.
func (p Plugin) Command(name string) (Command, bool) {
	for _, c := range p.Manifest.Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

var (
	pluginName  = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	commandName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	keyName     = regexp.MustCompile(`^ctrl\+alt\+[a-z0-9]$`)
)

// ValidName checks a plugin's name.
func ValidName(name string) error {
	if !pluginName.MatchString(name) {
		return fmt.Errorf("%w: name %q must be 1-%d characters of a-z and 0-9", ErrValidation, name, MaxNameLen)
	}
	return nil
}

// Check validates a manifest against the bundle's files, and fills each skill's name from its
// SKILL.md. Every contribution is checked here, at inspect, so an install cannot fail late on a
// malformed one (decision 2). detection checks one detection manifest's bytes.
func Check(m Manifest, files map[string][]byte, detection func([]byte) error) (Manifest, error) {
	bad := func(format string, a ...any) (Manifest, error) {
		return Manifest{}, fmt.Errorf("%w: "+format, append([]any{ErrValidation}, a...)...)
	}
	if err := ValidName(m.Name); err != nil {
		return Manifest{}, err
	}
	if n := len(m.Version); n == 0 || n > MaxVersionLen {
		return bad("version must be 1-%d characters", MaxVersionLen)
	}
	if n := len(m.Description); n == 0 || n > MaxDescriptionLen {
		return bad("description must be 1-%d characters", MaxDescriptionLen)
	}
	switch {
	case len(m.Commands) > MaxCommands:
		return bad("more than %d commands", MaxCommands)
	case len(m.Servers) > MaxServers:
		return bad("more than %d mcp_servers", MaxServers)
	case len(m.Skills) > MaxSkills:
		return bad("more than %d skills", MaxSkills)
	case len(m.Detection) > MaxDetection:
		return bad("more than %d detection manifests", MaxDetection)
	case len(m.Keys) > MaxKeys:
		return bad("more than %d keys", MaxKeys)
	}

	commands := map[string]bool{}
	for _, c := range m.Commands {
		if !commandName.MatchString(c.Name) {
			return bad("command %q: a name is 1-32 characters of a-z, 0-9 and '-', not starting with '-'", c.Name)
		}
		if commands[c.Name] {
			return bad("command %q is declared twice", c.Name)
		}
		commands[c.Name] = true
		if len(c.Argv) == 0 || len(c.Argv) > MaxArgv || strings.TrimSpace(c.Argv[0]) == "" {
			return bad("command %q: argv must be 1-%d words, the first not empty", c.Name, MaxArgv)
		}
		// A key types the line at a prompt (decision 8): a newline or an escape in a word would
		// run it, or rewrite what the user sees, before they press Enter.
		for _, w := range c.Argv {
			if strings.ContainsFunc(w, unicode.IsControl) {
				return bad("command %q: a word holds a control character", c.Name)
			}
		}
	}

	servers := map[string]bool{}
	for _, s := range m.Servers {
		if _, err := s.AddParams().Validate(); err != nil {
			return bad("mcp server %q: %v", s.Name, err)
		}
		if servers[s.Name] {
			return bad("mcp server %q is declared twice", s.Name)
		}
		servers[s.Name] = true
	}

	skills := map[string]bool{}
	out := m
	out.Skills = make([]Skill, 0, len(m.Skills))
	for _, sk := range m.Skills {
		name, digest, err := checkSkill(sk.Path, files)
		if err != nil {
			return Manifest{}, err
		}
		if skills[name] {
			return bad("skill %q is declared twice", name)
		}
		skills[name] = true
		out.Skills = append(out.Skills, Skill{Path: sk.Path, Name: name, SHA256: digest})
	}

	for _, d := range m.Detection {
		data, ok := files[d.Path]
		if !ok || !isEntry(d.Path) {
			return bad("detection %q is not a file of the bundle", d.Path)
		}
		if err := detection(data); err != nil {
			return bad("detection %q: %v", d.Path, err)
		}
	}

	keys := map[string]bool{}
	for _, k := range m.Keys {
		if !keyName.MatchString(k.Key) {
			return bad("key %q: a key is ctrl+alt+ and one of a-z or 0-9", k.Key)
		}
		if keys[k.Key] {
			return bad("key %q is bound twice", k.Key)
		}
		keys[k.Key] = true
		if !commands[k.Command] {
			return bad("key %q names %q, which is not one of the plugin's commands", k.Key, k.Command)
		}
	}
	return out, nil
}

// isEntry is a clean, relative, slash-separated path.
func isEntry(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.HasPrefix(p, "../") && p != ".."
}

// checkSkill reads the bundle's files under dir as a skill bundle, with the skill store's own
// checks, and returns the skill's name and digest.
func checkSkill(dir string, files map[string][]byte) (string, string, error) {
	if !isEntry(dir) {
		return "", "", fmt.Errorf("%w: skill %q: not a clean relative path of the bundle", ErrValidation, dir)
	}
	b := ctxdomain.NewBundleBuilder()
	prefix := dir + "/"
	sub := map[string][]byte{}
	for p, data := range files {
		if rel, ok := strings.CutPrefix(p, prefix); ok {
			if err := b.AddFile(rel, strings.NewReader(string(data))); err != nil {
				return "", "", fmt.Errorf("%w: skill %q: %w", ErrValidation, dir, err)
			}
			sub[rel] = data
		}
	}
	bundle, err := b.Finish()
	if err != nil {
		return "", "", fmt.Errorf("%w: skill %q: %w", ErrValidation, dir, err)
	}
	return bundle.Name, ctxdomain.Digest(sub), nil
}
