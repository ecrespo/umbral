// Package domain is the domain layer of the acp module, which hosts third-party agents that
// speak the Agent Client Protocol (REQ-ACP-*): their `[[acp.agents]]` entries, the manifests
// that make them ask before every side effect, and how their reports map onto Umbral's. It
// imports nothing outside the standard library and other modules' domain (Art. 3).
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// Refusal reasons: `data.reason` of the VALIDATION_ERROR thread.create answers (API §5.19).
const (
	ReasonUnknownAgent  = "unknown_agent"
	ReasonNoManifest    = "no_manifest"
	ReasonProjectConfig = "project_config"
	ReasonConfigKey     = "config_key"
)

// ErrRefused is a launch refused before anything ran; errors.As finds the *Refusal.
var ErrRefused = errors.New("acp: the agent was refused")

// ErrAgent is an agent that failed its turn: it exited, sent something that is not ACP, or
// fell silent (REQ-ACP-006). The turn ends `agent_error`.
var ErrAgent = errors.New("acp: the agent failed")

// Refusal is why an agent may not be launched (REQ-ACP-009).
type Refusal struct {
	Reason string
	Detail string
}

func (r *Refusal) Error() string { return r.Detail }

// Is makes errors.Is(err, ErrRefused) true for every refusal.
func (r *Refusal) Is(target error) bool { return target == ErrRefused }

func refuse(reason, format string, a ...any) error {
	return &Refusal{Reason: reason, Detail: fmt.Sprintf(format, a...)}
}

// Defaults and bounds of `[acp]` (REQ-ACP-005, REQ-ACP-006).
const (
	DefaultSilenceTimeout = 600 * time.Second
	DefaultMaxTerminals   = 4
	MinSilenceTimeoutS    = 5
	MaxSilenceTimeoutS    = 86400
	MinTerminals          = 1
	MaxTerminals          = 32
)

// Entry is one `[[acp.agents]]` of `config.toml` (REQ-ACP-001).
type Entry struct {
	Name string
	// Manifest names the manifest the agent is launched through; empty is the entry's name.
	Manifest string
	Command  string
	Args     []string
	// EnvRefs maps a variable of the agent's environment to `keyring:<path>` or `env:<VAR>`,
	// never to a value (Art. 5).
	EnvRefs map[string]string
	// Config is merged into the configuration the manifest generates, under the keys the
	// manifest lets an entry set: a provider and a model, never a permission.
	Config map[string]any
}

// ManifestName is the manifest the entry names.
func (e Entry) ManifestName() string {
	if e.Manifest != "" {
		return e.Manifest
	}
	return e.Name
}

var (
	entryName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	envName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate checks what config.toml can get wrong about an entry at start: its own fields, and
// the env_refs a built-in manifest reserves. A config key the manifest does not allow, and a
// manifest Umbral does not have, are refused when a thread is created (Host.Check).
func (e Entry) Validate() error {
	m, known := Builtin()[e.ManifestName()]
	if !entryName.MatchString(e.Name) {
		return fmt.Errorf("name %q must be 1-64 lowercase letters, digits or dashes", e.Name)
	}
	if strings.TrimSpace(e.Command) == "" {
		return fmt.Errorf("agent %q has no command", e.Name)
	}
	for name, ref := range e.EnvRefs {
		if !envName.MatchString(name) {
			return fmt.Errorf("agent %q: env_refs.%s is not a variable name", e.Name, name)
		}
		if Reserved(name) {
			return fmt.Errorf("agent %q: env_refs.%s is set by Umbral, not by an entry", e.Name, name)
		}
		if known && m.ReservesEnv(name) {
			return fmt.Errorf("agent %q: env_refs.%s configures the agent behind manifest %q; set a model through [acp.agents.config]",
				e.Name, name, m.Name)
		}
		if _, _, err := ParseRef(ref); err != nil {
			return fmt.Errorf("agent %q: env_refs.%s: %w", e.Name, name, err)
		}
	}
	return nil
}

// Reserved reports whether a variable is Umbral's to set in an agent's environment: the
// isolation, the locale and path, and every UMBRAL_* (REQ-ACP-009).
func Reserved(name string) bool {
	switch name {
	case "PATH", "HOME", "LANG":
		return true
	}
	return strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "UMBRAL_")
}

// Reference kinds of an env reference.
const (
	RefKeyring = "keyring"
	RefEnv     = "env"
)

// ParseRef splits `keyring:<path>` or `env:<VAR>`. Anything else is refused, so a value
// written where a reference belongs never reaches the agent.
func ParseRef(raw string) (kind, name string, err error) {
	kind, name, ok := strings.Cut(raw, ":")
	switch {
	case ok && kind == RefKeyring && strings.TrimSpace(name) != "":
		return kind, name, nil
	case ok && kind == RefEnv && envName.MatchString(name):
		return kind, name, nil
	default:
		return "", "", errors.New("a reference must be keyring:<path> or env:<VAR>, never a value")
	}
}

// ACP's tool kinds.
const (
	KindRead    = "read"
	KindEdit    = "edit"
	KindDelete  = "delete"
	KindMove    = "move"
	KindSearch  = "search"
	KindExecute = "execute"
	KindThink   = "think"
	KindFetch   = "fetch"
	KindOther   = "other"
)

// Kinds are ACP's tool kinds, the closed set a hosted call's `tool` is named from (API §4).
var Kinds = []string{KindRead, KindEdit, KindDelete, KindMove, KindSearch, KindExecute, KindThink, KindFetch, KindOther}

// Kind is the agent's kind if ACP has it, otherwise `other`: the SDK passes any string, and
// the row's name must not be the agent's to choose.
func Kind(kind string) string {
	for _, k := range Kinds {
		if k == kind {
			return k
		}
	}
	return KindOther
}

// Risk is the risk REQ-ACP-003 gives a tool call's kind. An unknown kind is Exec, so it is
// never decided more loosely than a command.
func Risk(kind string) secdomain.Risk {
	switch kind {
	case KindEdit, KindDelete, KindMove:
		return secdomain.RiskWriteFS
	case KindExecute:
		return secdomain.RiskExec
	case KindFetch:
		return secdomain.RiskNetwork
	case KindRead, KindSearch, KindThink:
		return secdomain.RiskReadOnly
	default:
		return secdomain.RiskExec
	}
}

// StopReason maps an ACP stop reason onto Umbral's (API §6). A refusal is the model's answer,
// so the turn ended normally; anything not in ACP's list is the agent's error.
func StopReason(acp string) string {
	switch acp {
	case "end_turn", "refusal":
		return "end_turn"
	case "max_tokens":
		return "budget"
	case "max_turn_requests":
		return "max_steps"
	case "cancelled":
		return "cancelled"
	default:
		return "agent_error"
	}
}

// ToolCall is a tool call the agent reports, as its `tool_call` and `tool_call_update`s have
// left it so far.
type ToolCall struct {
	// ID is the agent's tool call id, unique within its session.
	ID     string
	Title  string
	Kind   string
	Status string // pending, in_progress, completed, failed
	// RawInput is the agent's own description of the call, as JSON; Output its text content.
	RawInput  []byte
	Output    string
	Locations []string
}

// Ended reports whether the agent said the call finished.
func (c ToolCall) Ended() bool { return c.Status == "completed" || c.Status == "failed" }
