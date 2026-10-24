package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestTheACPAgentsAreRead_REQ_ACP_001: `[acp]` and `[[acp.agents]]` sit beside the subset's
// sections, with arrays and tables the subset itself refuses; the rest of the file still reads.
func TestTheACPAgentsAreRead_REQ_ACP_001(t *testing.T) {
	t.Parallel()

	settings, err := LoadSettings(nil, write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if settings.ACP.SilenceTimeoutS != 600 || settings.ACP.MaxTerminals != 4 || len(settings.ACP.Agents) != 0 {
		t.Fatalf("default [acp] = %+v, want 600 s, 4 terminals, no agent", settings.ACP)
	}

	settings, err = LoadSettings(nil, write(t, `
[agents]
max_concurrent_turns = 5

[acp]
silence_timeout = 120  # two minutes
max_terminals = 2

[[acp.agents]]
name = "opencode"
command = "opencode"
args = ["acp"]
env_refs = { OPENROUTER_API_KEY = "keyring:umbral/openrouter" }

[acp.agents.config]
model = "ollama/gpt-oss:20b"

[[acp.agents]]
name = "other"
manifest = "opencode"
command = "/opt/other/bin/agent"

[notifications]
enabled = false
`))
	if err != nil {
		t.Fatal(err)
	}
	if settings.MaxConcurrentTurns != 5 || settings.NotificationsEnabled {
		t.Errorf("the sections around [acp] were not read: %+v", settings)
	}
	acp := settings.ACP
	if acp.SilenceTimeoutS != 120 || acp.MaxTerminals != 2 || len(acp.Agents) != 2 {
		t.Fatalf("[acp] = %+v", acp)
	}
	first := acp.Agents[0]
	if first.Name != "opencode" || first.Command != "opencode" || !reflect.DeepEqual(first.Args, []string{"acp"}) ||
		first.EnvRefs["OPENROUTER_API_KEY"] != "keyring:umbral/openrouter" || first.Config["model"] != "ollama/gpt-oss:20b" ||
		first.ManifestName() != "opencode" {
		t.Errorf("the first agent is %+v", first)
	}
	if acp.Agents[1].ManifestName() != "opencode" {
		t.Errorf("the second agent's manifest is %q", acp.Agents[1].ManifestName())
	}
}

// TestABadACPEntryStopsTheDaemon_REQ_ACP_009: what the file gets wrong about an agent is an
// error naming it, never an agent launched some other way.
func TestABadACPEntryStopsTheDaemon_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"a value for a reference": "[[acp.agents]]\nname = \"a\"\ncommand = \"x\"\nenv_refs = { TOKEN = \"sk-123\" }\n",
		"a reserved variable":     "[[acp.agents]]\nname = \"a\"\ncommand = \"x\"\nenv_refs = { HOME = \"env:HOME\" }\n",
		"an UMBRAL_ variable":     "[[acp.agents]]\nname = \"a\"\ncommand = \"x\"\nenv_refs = { UMBRAL_PANE_ID = \"env:X\" }\n",
		"no command":              "[[acp.agents]]\nname = \"a\"\n",
		"a bad name":              "[[acp.agents]]\nname = \"A B\"\ncommand = \"x\"\n",
		"the agent's own config":  "[[acp.agents]]\nname = \"opencode\"\ncommand = \"x\"\nenv_refs = { OPENCODE_CONFIG_CONTENT = \"env:X\" }\n",
		"its permission override": "[[acp.agents]]\nname = \"a\"\nmanifest = \"opencode\"\ncommand = \"x\"\nenv_refs = { OPENCODE_PERMISSION = \"env:X\" }\n",
		"twice":                   "[[acp.agents]]\nname = \"a\"\ncommand = \"x\"\n[[acp.agents]]\nname = \"a\"\ncommand = \"y\"\n",
		"an unknown key":          "[acp]\nsilence = 3\n",
		"a short timeout":         "[acp]\nsilence_timeout = 1\n",
		"too many terminals":      "[acp]\nmax_terminals = 100\n",
	} {
		_, err := LoadSettings(nil, write(t, text))
		if !errors.Is(err, ErrSettingsInvalid) {
			t.Errorf("%s: %v, want ErrSettingsInvalid", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "sk-123") {
			t.Errorf("%s: the error repeats the value: %v", name, err)
		}
	}
}
