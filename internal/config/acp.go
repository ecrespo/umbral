package config

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	acpdomain "github.com/ecrespo/umbral/internal/acp/domain"
)

// ACP is `[acp]` and `[[acp.agents]]` of config.toml: the third-party agents Umbral hosts
// (REQ-ACP-001) and the limits they run under (REQ-ACP-005, REQ-ACP-006). Read at start.
type ACP struct {
	// SilenceTimeoutS is `[acp] silence_timeout`, in seconds.
	SilenceTimeoutS int
	// MaxTerminals is `[acp] max_terminals`, per thread.
	MaxTerminals int
	Agents       []acpdomain.Entry
}

// DefaultACP is `[acp]` when config.toml says nothing: no agent, and the PRD's limits.
func DefaultACP() ACP {
	return ACP{SilenceTimeoutS: int(acpdomain.DefaultSilenceTimeout.Seconds()), MaxTerminals: acpdomain.DefaultMaxTerminals}
}

// splitACP takes `[acp]`, `[[acp.agents]]` and their sub-tables, and `[sandbox]`, out of
// config.toml's text.
// They need arrays and tables, which the rest of the file's subset refuses, so they are read
// with a TOML library. Each line goes to one side and leaves a blank on the other, so a parse
// error on either still names the file's own line.
func splitACP(text string) (rest, acp string) {
	var r, a strings.Builder
	inACP := false
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			name := strings.Trim(trimmed, "[] \t")
			if i := strings.Index(trimmed, "#"); i >= 0 {
				name = strings.Trim(trimmed[:i], "[] \t")
			}
			inACP = name == "acp" || strings.HasPrefix(name, "acp.") || name == "sandbox"
		}
		blank := ""
		if strings.HasSuffix(line, "\n") {
			blank = "\n"
		}
		if inACP {
			a.WriteString(line)
			r.WriteString(blank)
		} else {
			r.WriteString(line)
			a.WriteString(blank)
		}
	}
	return r.String(), a.String()
}

// acpFile is the TOML shape of the ACP tables.
type acpFile struct {
	ACP struct {
		SilenceTimeout *int `toml:"silence_timeout"`
		MaxTerminals   *int `toml:"max_terminals"`
		Agents         []struct {
			Name     string            `toml:"name"`
			Manifest string            `toml:"manifest"`
			Command  string            `toml:"command"`
			Args     []string          `toml:"args"`
			EnvRefs  map[string]string `toml:"env_refs"`
			Config   map[string]any    `toml:"config"`
			Relay    string            `toml:"relay"`
		} `toml:"agents"`
	} `toml:"acp"`
	Sandbox struct {
		Mode  *string  `toml:"mode"`
		Write []string `toml:"write"`
	} `toml:"sandbox"`
}

// Sandbox is `[sandbox]` of config.toml (REQ-SBX-003, REQ-SBX-004). Read at start.
type Sandbox struct {
	// Off is `mode = "off"`: nothing is confined, and every thread it affects says so. The
	// default, `enforce`, confines wherever the system can.
	Off bool
	// Write is `write`: directories a confined process may write beyond its profile — build
	// caches are the expected entries. Each is a clean absolute path; the daemon refuses at
	// start one that reaches a directory it protects.
	Write []string
}

// The values of `[sandbox] mode`.
const (
	SandboxEnforce = "enforce"
	SandboxOff     = "off"
)

// parseACP reads the ACP tables and `[sandbox]` strictly: an unknown key, a value of the wrong type, an entry
// that names no command or holds a value where a reference belongs, all stop the daemon, as
// the rest of config.toml does.
func parseACP(text string) (ACP, Sandbox, error) {
	out := DefaultACP()
	var sandbox Sandbox
	if strings.TrimSpace(text) == "" {
		return out, sandbox, nil
	}
	var f acpFile
	dec := toml.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return ACP{}, Sandbox{}, fmt.Errorf("acp or sandbox: %s", strings.TrimSpace(strict.String()))
		}
		return ACP{}, Sandbox{}, fmt.Errorf("acp or sandbox: %w", err)
	}
	if v := f.ACP.SilenceTimeout; v != nil {
		if *v < acpdomain.MinSilenceTimeoutS || *v > acpdomain.MaxSilenceTimeoutS {
			return ACP{}, Sandbox{}, fmt.Errorf("acp.silence_timeout is %d, want whole seconds from %d to %d",
				*v, acpdomain.MinSilenceTimeoutS, acpdomain.MaxSilenceTimeoutS)
		}
		out.SilenceTimeoutS = *v
	}
	if v := f.ACP.MaxTerminals; v != nil {
		if *v < acpdomain.MinTerminals || *v > acpdomain.MaxTerminals {
			return ACP{}, Sandbox{}, fmt.Errorf("acp.max_terminals is %d, want a whole number from %d to %d",
				*v, acpdomain.MinTerminals, acpdomain.MaxTerminals)
		}
		out.MaxTerminals = *v
	}
	seen := map[string]bool{}
	for _, a := range f.ACP.Agents {
		e := acpdomain.Entry{
			Name: a.Name, Manifest: a.Manifest, Command: a.Command, Args: a.Args, EnvRefs: a.EnvRefs, Config: a.Config, Relay: a.Relay,
		}
		if err := e.Validate(); err != nil {
			return ACP{}, Sandbox{}, fmt.Errorf("acp.agents: %w", err)
		}
		if seen[e.Name] {
			return ACP{}, Sandbox{}, fmt.Errorf("acp.agents: %q is listed twice", e.Name)
		}
		seen[e.Name] = true
		out.Agents = append(out.Agents, e)
	}
	if v := f.Sandbox.Mode; v != nil {
		switch *v {
		case SandboxEnforce:
		case SandboxOff:
			sandbox.Off = true
		default:
			return ACP{}, Sandbox{}, fmt.Errorf("sandbox.mode is %q, want %q or %q", *v, SandboxEnforce, SandboxOff)
		}
	}
	for _, w := range f.Sandbox.Write {
		if !filepath.IsAbs(w) || filepath.Clean(w) != w {
			return ACP{}, Sandbox{}, fmt.Errorf("sandbox.write: %q is not a clean absolute path", w)
		}
		sandbox.Write = append(sandbox.Write, w)
	}
	return out, sandbox, nil
}
