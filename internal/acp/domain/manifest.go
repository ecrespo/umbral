package domain

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Manifest is how Umbral launches one agent so that it asks before every side effect
// (REQ-ACP-009): the configuration it generates, where the agent reads it, and the files
// through which a repository would configure the agent behind Umbral's back.
type Manifest struct {
	Name string
	// ConfigPath is where the generated configuration is written, relative to the agent's
	// isolated XDG_CONFIG_HOME; ConfigEnv, when set, is a variable naming it as well.
	ConfigPath string
	ConfigEnv  string
	// ProjectConfig are the entries a repository configures the agent with. Found in the launch
	// directory or above it, they refuse the launch (`project_config`): the agent would read
	// them after Umbral's configuration, and could set a permission back to `allow`.
	ProjectConfig []string
	// UserKeys are the keys an entry's `config` may set: how to reach a model, nothing that
	// decides what the agent may do.
	UserKeys []string
	// EnvPrefixes are the variables the agent reads its configuration from, which an entry's
	// env_refs may not name: through them it could set a permission back to `allow` or load a
	// plugin, around UserKeys.
	EnvPrefixes []string
	// FixedEnv are variables the agent always gets, set after the entry's env refs and
	// reserved like EnvPrefixes, so no ref names or overrides one: switches that keep the agent
	// from reaching the network on its own initiative (T-HRD-11).
	FixedEnv map[string]string
	// Fixed is the configuration Umbral imposes. It is written over the entry's, so no
	// UserKeys may name one of its keys.
	Fixed map[string]any
}

// Generate is the configuration file the agent reads: the entry's own keys, then Umbral's.
func (m Manifest) Generate(entry map[string]any) ([]byte, error) {
	out := map[string]any{}
	keys := make([]string, 0, len(entry))
	for k := range entry {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(m.UserKeys, k) {
			return nil, refuse(ReasonConfigKey, "config.%s is not a key an [[acp.agents]] entry of manifest %q may set (allowed: %v)",
				k, m.Name, m.UserKeys)
		}
		out[k] = entry[k]
	}
	for k, v := range m.Fixed {
		out[k] = v
	}
	return json.MarshalIndent(out, "", "  ")
}

// ReservesEnv reports whether name is one of the variables the agent reads its configuration
// from, or the one naming Umbral's.
func (m Manifest) ReservesEnv(name string) bool {
	if m.ConfigEnv != "" && name == m.ConfigEnv {
		return true
	}
	if _, ok := m.FixedEnv[name]; ok {
		return true
	}
	for _, prefix := range m.EnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ManifestOpenCode is the built-in manifest's name.
const ManifestOpenCode = "opencode"

// OpenCodePermissions are the permissions the OpenCode manifest sets to `ask`. The wildcard
// comes first, since OpenCode's last matching rule wins, and covers every tool it has —
// MCP tools included — the named ones make the list explicit for a reader and for a version
// that does not read the wildcard.
var OpenCodePermissions = []string{"edit", "bash", "webfetch", "websearch", "external_directory", "task"}

// Builtin are the manifests Umbral ships (F2a: OpenCode, the agent spike T-F2-00 measured).
func Builtin() map[string]Manifest {
	return map[string]Manifest{ManifestOpenCode: openCode()}
}

func openCode() Manifest {
	// OpenCode reads `permission` in order and encoding/json sorts a map's keys, so orderedAsk
	// writes the wildcard first.
	return Manifest{
		Name:          ManifestOpenCode,
		ConfigPath:    filepath.Join("opencode", "opencode.json"),
		ConfigEnv:     "OPENCODE_CONFIG",
		ProjectConfig: []string{"opencode.json", "opencode.jsonc", ".opencode"},
		UserKeys:      []string{"model", "small_model", "provider", "enabled_providers", "disabled_providers"},
		// OPENCODE_CONFIG, OPENCODE_CONFIG_CONTENT, OPENCODE_CONFIG_DIR, OPENCODE_PERMISSION and
		// whatever flag a later version adds.
		EnvPrefixes: []string{"OPENCODE_"},
		// What OpenCode fetches at start on its own: its updates, its default plugins from npm,
		// language servers, the models.dev catalogue; and sharing a session (PRD F2 §9).
		FixedEnv: map[string]string{
			"OPENCODE_DISABLE_AUTOUPDATE":      "1",
			"OPENCODE_DISABLE_DEFAULT_PLUGINS": "1",
			"OPENCODE_DISABLE_LSP_DOWNLOAD":    "1",
			"OPENCODE_DISABLE_MODELS_FETCH":    "1",
			"OPENCODE_DISABLE_SHARE":           "1",
		},
		Fixed: map[string]any{
			"$schema":    "https://opencode.ai/config.json",
			"permission": orderedAsk(OpenCodePermissions),
			// No MCP server and no plugin of the agent's own: its tools are the ones the
			// permissions above cover, and nothing loads code Umbral did not choose.
			"mcp":    map[string]any{},
			"plugin": []string{},
			// Nothing leaves the machine on the agent's initiative beyond its model calls.
			"autoupdate": false,
			"share":      "disabled",
		},
	}
}

// orderedAsk is a JSON object of "*": "ask" followed by each name: "ask", in that order.
type orderedAsk []string

// MarshalJSON writes the wildcard first.
func (o orderedAsk) MarshalJSON() ([]byte, error) {
	buf := []byte(`{"*":"ask"`)
	for _, name := range o {
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf = append(buf, ',')
		buf = append(buf, key...)
		buf = append(buf, `:"ask"`...)
	}
	return append(buf, '}'), nil
}
