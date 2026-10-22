package domain

import (
	"errors"
	"testing"
)

func TestBuiltinManifestsCompile_REQ_XAG_001(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, m := range Builtins() {
		names[m.Name] = true
		if m.Origin != OriginBuiltin {
			t.Errorf("%s: origin %q", m.Name, m.Origin)
		}
	}
	for _, want := range []string{"opencode", "claude-code", "gemini-cli", "codex", "goose"} {
		if !names[want] {
			t.Errorf("no built-in manifest for %s", want)
		}
	}
}

// TestBuiltinsRecogniseTheAgentsAndNothingElse_REQ_XAG_001: each built-in recognises its agent,
// launched directly or through node, and a command that merely names it does not.
func TestBuiltinsRecogniseTheAgentsAndNothingElse_REQ_XAG_001(t *testing.T) {
	t.Parallel()
	for argv, want := range map[string]string{
		"/usr/local/bin/opencode":    "opencode",
		"opencode run fix the tests": "opencode",
		"/usr/bin/node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js": "claude-code",
		"claude --resume":              "claude-code",
		"node /home/u/.npm/bin/gemini": "gemini-cli",
		"codex exec":                   "codex",
		"/opt/goose/bin/goose session": "goose",
		"vim claude.md":                "",
		"less /tmp/opencode":           "",
		"git commit -m codex":          "",
		"node server.js --name gemini": "",
	} {
		d, ok := Detect(Builtins(), []Process{{PID: 7, Argv: splitArgv(argv)}})
		got := ""
		if ok {
			got = d.Manifest.Name
		}
		if got != want {
			t.Errorf("%q: detected %q, want %q", argv, got, want)
		}
	}
}

func splitArgv(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

// TestDetectionIsDeterministic_REQ_XAG_003: the first process in the order given wins, and for
// one process the manifests are tried in name order with executables first, so explain names
// the same rule every time.
func TestDetectionIsDeterministic_REQ_XAG_003(t *testing.T) {
	t.Parallel()
	a, _ := Manifest{Name: "b-agent", StateSource: AgentStateNone, Executables: []string{"tool"}}.Compile()
	b, _ := Manifest{Name: "a-agent", StateSource: AgentStateNone, Argv: []string{`^tool`}}.Compile()
	procs := []Process{{PID: 2, Argv: []string{"sh"}}, {PID: 3, Argv: []string{"tool"}}, {PID: 4, Argv: []string{"goose"}}}
	for range 20 {
		d, ok := Detect([]Manifest{a, b}, procs)
		if !ok || d.Manifest.Name != "a-agent" || d.Rule != RuleArgv || d.Pattern != `^tool` || d.Process.PID != 3 {
			t.Fatalf("detection %+v", d)
		}
	}
}

func TestManifestValidation_REQ_XAG_002(t *testing.T) {
	t.Parallel()
	for name, m := range map[string]Manifest{
		"bad name":         {Name: "Bad Name", StateSource: AgentStateNone, Executables: []string{"x"}},
		"bad state source": {Name: "x", StateSource: "screen", Executables: []string{"x"}},
		"no rule":          {Name: "x", StateSource: AgentStateNone},
		"a path":           {Name: "x", StateSource: AgentStateNone, Executables: []string{"/bin/x"}},
		"a bad pattern":    {Name: "x", StateSource: AgentStateNone, Argv: []string{"("}},
	} {
		if _, err := m.Compile(); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOverlayReplacesByName_REQ_XAG_002(t *testing.T) {
	t.Parallel()
	local, _ := Manifest{Name: "goose", StateSource: AgentStateHooks, Executables: []string{"goose2"}, Origin: "/x/goose.toml"}.Compile()
	extra, _ := Manifest{Name: "aider", StateSource: AgentStateNone, Executables: []string{"aider"}, Origin: "/x/aider.toml"}.Compile()
	got := Overlay(Builtins(), []Manifest{local, extra})
	byName := map[string]Manifest{}
	for _, m := range got {
		byName[m.Name] = m
	}
	if byName["goose"].Origin != "/x/goose.toml" || byName["aider"].Origin != "/x/aider.toml" || byName["codex"].Origin != OriginBuiltin {
		t.Errorf("overlay %+v", got)
	}
	if len(got) != len(Builtins())+1 {
		t.Errorf("%d manifests", len(got))
	}
}
