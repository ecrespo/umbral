package domain

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Isolation is the agent's own home and XDG directories under the daemon's runtime directory
// (REQ-ACP-009), so the user's global agent configuration is never read.
type Isolation struct {
	Root string
}

// Home is the agent's HOME.
func (i Isolation) Home() string { return filepath.Join(i.Root, "home") }

// Config is its XDG_CONFIG_HOME, where the generated configuration is written.
func (i Isolation) Config() string { return filepath.Join(i.Root, "config") }

// Data is its XDG_DATA_HOME.
func (i Isolation) Data() string { return filepath.Join(i.Root, "data") }

// Cache is its XDG_CACHE_HOME.
func (i Isolation) Cache() string { return filepath.Join(i.Root, "cache") }

// State is its XDG_STATE_HOME.
func (i Isolation) State() string { return filepath.Join(i.Root, "state") }

// Dirs are the directories to create, in order.
func (i Isolation) Dirs() []string {
	return []string{i.Root, i.Home(), i.Config(), i.Data(), i.Cache(), i.State()}
}

// Inherited are the daemon's variables an agent gets: what finds its programs and its locale.
// Nothing else crosses — a provider key the env fallback put in the daemon's environment
// included — and the entry's env_refs are the only way to give the agent a credential.
var Inherited = []string{"PATH", "LANG"}

// Environment is the agent process's whole environment (REQ-ACP-009): Inherited from the
// daemon's, the isolation, the generated configuration's location, and the resolved refs.
func Environment(daemon []string, iso Isolation, m Manifest, refs map[string]string) []string {
	env := map[string]string{}
	for _, kv := range daemon {
		name, value, ok := strings.Cut(kv, "=")
		if ok && slices.Contains(Inherited, name) {
			env[name] = value
		}
	}
	for name, value := range refs {
		if !Reserved(name) && !m.ReservesEnv(name) {
			env[name] = value
		}
	}
	env["HOME"] = iso.Home()
	env["XDG_CONFIG_HOME"] = iso.Config()
	env["XDG_DATA_HOME"] = iso.Data()
	env["XDG_CACHE_HOME"] = iso.Cache()
	env["XDG_STATE_HOME"] = iso.State()
	if m.ConfigEnv != "" {
		env[m.ConfigEnv] = filepath.Join(iso.Config(), m.ConfigPath)
	}
	out := make([]string, 0, len(env))
	for name, value := range env {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}
