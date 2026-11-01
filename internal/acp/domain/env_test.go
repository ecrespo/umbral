package domain

import (
	"slices"
	"testing"
)

// TestTheManifestsFixedVariablesReachTheAgent_REQ_ACP_009: the variables a manifest fixes —
// for OpenCode, the switches that keep it from fetching updates, plugins, language servers and
// a model catalogue on its own — are in the agent's environment whatever an entry's env refs
// say, so no ref can turn that traffic back on (delta `2026-10-opencode-fixed-env`).
func TestTheManifestsFixedVariablesReachTheAgent_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	m := Builtin()[ManifestOpenCode]
	for _, name := range []string{
		"OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_DISABLE_DEFAULT_PLUGINS", "OPENCODE_DISABLE_LSP_DOWNLOAD",
		"OPENCODE_DISABLE_MODELS_FETCH", "OPENCODE_DISABLE_SHARE",
	} {
		if m.FixedEnv[name] != "1" {
			t.Errorf("the OpenCode manifest does not fix %s=1: %q", name, m.FixedEnv[name])
		}
	}

	// A manifest whose fixed variable falls outside its reserved prefixes still wins over a ref.
	m.FixedEnv = map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "1", "AGENT_TELEMETRY": "off"}
	refs := map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "0", "AGENT_TELEMETRY": "on", "MODEL_TOKEN": "t"}
	// A ref naming one is refused at start, like one under the manifest's prefixes.
	if !m.ReservesEnv("AGENT_TELEMETRY") {
		t.Error("a fixed variable is not reserved, so an entry's ref may name it")
	}
	env := Environment([]string{"PATH=/bin"}, Isolation{Root: "/run/acp/t"}, m, refs)
	for _, want := range []string{"OPENCODE_DISABLE_AUTOUPDATE=1", "AGENT_TELEMETRY=off", "MODEL_TOKEN=t"} {
		if !slices.Contains(env, want) {
			t.Errorf("the agent's environment lacks %s: %v", want, env)
		}
	}
	for _, unwanted := range []string{"OPENCODE_DISABLE_AUTOUPDATE=0", "AGENT_TELEMETRY=on"} {
		if slices.Contains(env, unwanted) {
			t.Errorf("a ref overrode a fixed variable: %s", unwanted)
		}
	}
}
