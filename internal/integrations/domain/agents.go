package domain

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Third-party agent detection (PRD F2 REQ-XAG-001…003). A manifest says how to recognise an
// agent among a pane's processes and how that agent reports its state. Detection only labels a
// pane: under DD-012 a pane's state still comes from its own thread, then external reports, then
// the shell, and nothing here is an input to ResolveAttention.

// The state sources a manifest can declare: how the agent tells Umbral what it is doing.
const (
	AgentStateHooks = "hooks" // its hooks call `umb pane report_state`
	AgentStateACP   = "acp"   // it speaks ACP to Umbral
	AgentStateNone  = "none"  // it reports nothing
)

// OriginBuiltin is the Origin of a manifest Umbral ships; a local one's is its file's path.
const OriginBuiltin = "builtin"

// The bounds a manifest is checked against.
const (
	maxManifestRules   = 16
	maxManifestPattern = 512
)

// Manifest is one agent's detection manifest.
type Manifest struct {
	Name        string // [a-z0-9][a-z0-9-]*, at most 32 characters; local files override by it
	DisplayName string
	StateSource string // AgentStateHooks, AgentStateACP or AgentStateNone
	// Executables are basenames compared with the basename of a process's argv[0].
	Executables []string
	// Argv are RE2 patterns matched against a process's argv joined by single spaces.
	Argv   []string
	Origin string

	argv []*regexp.Regexp
}

// ErrInvalidManifest is a manifest that cannot be used; the whole file is skipped.
var ErrInvalidManifest = errors.New("invalid agent manifest")

var manifestName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Compile validates a manifest and prepares its patterns. An error names the field and the rule
// it breaks and never quotes the value: it is logged and answered, and a value can be anything
// the user pasted.
func (m Manifest) Compile() (Manifest, error) {
	fail := func(format string, args ...any) (Manifest, error) {
		return Manifest{}, fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
	}
	if !manifestName.MatchString(m.Name) {
		return fail("name is not lower-case letters, digits and hyphens, at most 32")
	}
	switch m.StateSource {
	case AgentStateHooks, AgentStateACP, AgentStateNone:
	default:
		return fail("state_source is not hooks, acp or none")
	}
	if n := len(m.Executables) + len(m.Argv); n == 0 || n > maxManifestRules {
		return fail("%d detection rules; between 1 and %d are allowed", n, maxManifestRules)
	}
	for i, e := range m.Executables {
		if e == "" || strings.ContainsAny(e, "/\x00") {
			return fail("executable %d is not a basename", i+1)
		}
	}
	m.argv = make([]*regexp.Regexp, 0, len(m.Argv))
	for i, p := range m.Argv {
		if p == "" || len(p) > maxManifestPattern {
			return fail("argv pattern %d must have 1 to %d bytes", i+1, maxManifestPattern)
		}
		// The compiler's message quotes the pattern, so only its position is reported.
		re, err := regexp.Compile(p)
		if err != nil {
			return fail("argv pattern %d is not a valid RE2 expression", i+1)
		}
		m.argv = append(m.argv, re)
	}
	if m.DisplayName == "" {
		m.DisplayName = m.Name
	}
	return m, nil
}

// Process is one process under a pane's shell.
type Process struct {
	PID  int
	Argv []string
}

// Rule kinds, as agent.explain names them.
const (
	RuleExecutable = "executable"
	RuleArgv       = "argv"
)

// Detection is a manifest that matched one of a pane's processes, and why.
type Detection struct {
	Manifest Manifest
	Rule     string // RuleExecutable or RuleArgv
	Pattern  string // the executable name or the argv pattern that matched
	Process  Process
}

// Detect returns the first match of manifests among procs. Processes are taken in the order
// given — the caller lists them breadth first from the shell, so the agent the user started
// wins over anything it starts — and for each process the manifests in name order, an
// executable rule before an argv rule, so the answer never depends on map order.
func Detect(manifests []Manifest, procs []Process) (Detection, bool) {
	sorted := append([]Manifest(nil), manifests...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, p := range procs {
		if len(p.Argv) == 0 {
			continue
		}
		exe := path.Base(p.Argv[0])
		line := strings.Join(p.Argv, " ")
		for _, m := range sorted {
			for _, e := range m.Executables {
				if e == exe {
					return Detection{Manifest: m, Rule: RuleExecutable, Pattern: e, Process: p}, true
				}
			}
			for i, re := range m.argv {
				if re.MatchString(line) {
					return Detection{Manifest: m, Rule: RuleArgv, Pattern: m.Argv[i], Process: p}, true
				}
			}
		}
	}
	return Detection{}, false
}

// Overlay lays local manifests over base by name: a local manifest replaces the built-in one of
// the same name and adds the others. The result is in name order.
func Overlay(base, local []Manifest) []Manifest {
	byName := map[string]Manifest{}
	for _, m := range base {
		byName[m.Name] = m
	}
	for _, m := range local {
		byName[m.Name] = m
	}
	out := make([]Manifest, 0, len(byName))
	for _, m := range byName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Builtins are the manifests Umbral ships (REQ-XAG-001). A Node-hosted agent runs as `node
// <its script>`, so its argv pattern names the package or the script, never a bare word an
// argument such as a file name could carry.
func Builtins() []Manifest {
	raw := []Manifest{
		{
			Name: "claude-code", DisplayName: "Claude Code", StateSource: AgentStateHooks,
			Executables: []string{"claude"},
			Argv:        []string{`^\S*node\S*\s+\S*(@anthropic-ai/claude-code/|/claude(\s|$))`},
		},
		{
			Name: "codex", DisplayName: "Codex", StateSource: AgentStateHooks,
			Executables: []string{"codex"},
			Argv:        []string{`^\S*node\S*\s+\S*(@openai/codex/|/codex(\s|$))`},
		},
		{
			Name: "gemini-cli", DisplayName: "Gemini CLI", StateSource: AgentStateHooks,
			Executables: []string{"gemini"},
			Argv:        []string{`^\S*node\S*\s+\S*(@google/gemini-cli/|/gemini(\s|$))`},
		},
		{
			Name: "goose", DisplayName: "Goose", StateSource: AgentStateNone,
			Executables: []string{"goose"},
		},
		{
			Name: "opencode", DisplayName: "OpenCode", StateSource: AgentStateACP,
			Executables: []string{"opencode"},
			Argv:        []string{`^\S*node\S*\s+\S*(opencode-ai/|/opencode(\s|$))`},
		},
	}
	out := make([]Manifest, 0, len(raw))
	for _, m := range raw {
		m.Origin = OriginBuiltin
		c, err := m.Compile()
		if err != nil {
			panic(err) // a built-in manifest is a constant; TestBuiltinManifestsCompile guards it
		}
		out = append(out, c)
	}
	return out
}

// SkippedManifest is a local manifest file that could not be used, and why (REQ-XAG-002).
type SkippedManifest struct {
	File   string
	Reason string
}

// ExplainedDetection is a detection as agent.explain answers it: the process is named by its
// pid and its executable's basename, never by its argv, which can carry a secret.
type ExplainedDetection struct {
	Manifest   Manifest
	Rule       string
	Pattern    string
	PID        int
	Executable string
}

// Explanation is agent.explain's answer for a pane (REQ-XAG-003).
type Explanation struct {
	PaneID    string
	Detection *ExplainedDetection // nil when nothing matched
	// AttentionState and StateSource are the pane's state and who decided it, under DD-012;
	// StateSource is "" when nothing does.
	AttentionState string
	StateSource    string
	Skipped        []SkippedManifest
}
