package domain

import (
	"encoding/json"
	"net/url"
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
	// PortFlag is the flag that gives the agent the loopback port it serves itself on; ""
	// is a manifest that cannot be given a relay, since its port could not be named in a
	// network profile.
	PortFlag string
	// Relay points the entry's configuration at a relay (REQ-ACP-009): upstream is the
	// models.toml provider's base URL, origin the relay's `http://host:port`, token its
	// bearer token. It returns a copy and leaves entry as it was; nil is a manifest that
	// cannot be given a relay.
	Relay func(entry map[string]any, upstream, origin, token string) (map[string]any, error)
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
		PortFlag: "--port",
		Relay:    openCodeRelay,
	}
}

// openCodeRelay points the provider of the entry's `model` at the relay: its
// `options.baseURL`, which must be the upstream's endpoint or below it, gets the relay's origin
// with its own path kept, and `options.apiKey` the relay's token.
func openCodeRelay(entry map[string]any, upstream, origin, token string) (map[string]any, error) {
	model, _ := entry["model"].(string)
	name, _, ok := strings.Cut(model, "/")
	if !ok || name == "" {
		return nil, refuse(ReasonRelay, "config.model %q does not name a provider as <provider>/<model>", model)
	}
	providers, _ := entry["provider"].(map[string]any)
	provider, ok := providers[name].(map[string]any)
	if !ok {
		return nil, refuse(ReasonRelay, "config.provider.%s is not set: the relay needs the endpoint the agent reaches its model at", name)
	}
	options, _ := provider["options"].(map[string]any)
	base, _ := options["baseURL"].(string)
	b, err := url.Parse(base)
	if err != nil || base == "" {
		return nil, refuse(ReasonRelay, "config.provider.%s.options.baseURL %q is not a URL", name, base)
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, refuse(ReasonRelay, "the relay's provider has no endpoint to compare with: %v", err)
	}
	up := strings.TrimSuffix(u.EscapedPath(), "/")
	path := b.EscapedPath()
	below := path == up || strings.HasPrefix(path, up+"/")
	if b.Scheme != u.Scheme || !strings.EqualFold(b.Host, u.Host) || b.User != nil || b.RawQuery != "" || b.Fragment != "" || !below {
		return nil, refuse(ReasonRelay, "config.provider.%s.options.baseURL %q is not the relay's provider's endpoint %q or below it",
			name, base, upstream)
	}
	outOptions := map[string]any{}
	for k, v := range options {
		outOptions[k] = v
	}
	outOptions["baseURL"] = strings.TrimSuffix(origin, "/") + path
	outOptions["apiKey"] = token
	outProvider := map[string]any{}
	for k, v := range provider {
		outProvider[k] = v
	}
	outProvider["options"] = outOptions
	outProviders := map[string]any{}
	for k, v := range providers {
		outProviders[k] = v
	}
	outProviders[name] = outProvider
	out := map[string]any{}
	for k, v := range entry {
		out[k] = v
	}
	out["provider"] = outProviders
	return out, nil
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
