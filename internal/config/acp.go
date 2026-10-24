package config

import (
	"bytes"
	"errors"
	"fmt"
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

// splitACP takes `[acp]`, `[[acp.agents]]` and their sub-tables out of config.toml's text.
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
			inACP = name == "acp" || strings.HasPrefix(name, "acp.")
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
		} `toml:"agents"`
	} `toml:"acp"`
}

// parseACP reads the ACP tables strictly: an unknown key, a value of the wrong type, an entry
// that names no command or holds a value where a reference belongs, all stop the daemon, as
// the rest of config.toml does.
func parseACP(text string) (ACP, error) {
	out := DefaultACP()
	if strings.TrimSpace(text) == "" {
		return out, nil
	}
	var f acpFile
	dec := toml.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return ACP{}, fmt.Errorf("acp: %s", strings.TrimSpace(strict.String()))
		}
		return ACP{}, fmt.Errorf("acp: %w", err)
	}
	if v := f.ACP.SilenceTimeout; v != nil {
		if *v < acpdomain.MinSilenceTimeoutS || *v > acpdomain.MaxSilenceTimeoutS {
			return ACP{}, fmt.Errorf("acp.silence_timeout is %d, want whole seconds from %d to %d",
				*v, acpdomain.MinSilenceTimeoutS, acpdomain.MaxSilenceTimeoutS)
		}
		out.SilenceTimeoutS = *v
	}
	if v := f.ACP.MaxTerminals; v != nil {
		if *v < acpdomain.MinTerminals || *v > acpdomain.MaxTerminals {
			return ACP{}, fmt.Errorf("acp.max_terminals is %d, want a whole number from %d to %d",
				*v, acpdomain.MinTerminals, acpdomain.MaxTerminals)
		}
		out.MaxTerminals = *v
	}
	seen := map[string]bool{}
	for _, a := range f.ACP.Agents {
		e := acpdomain.Entry{
			Name: a.Name, Manifest: a.Manifest, Command: a.Command, Args: a.Args, EnvRefs: a.EnvRefs, Config: a.Config,
		}
		if err := e.Validate(); err != nil {
			return ACP{}, fmt.Errorf("acp.agents: %w", err)
		}
		if seen[e.Name] {
			return ACP{}, fmt.Errorf("acp.agents: %q is listed twice", e.Name)
		}
		seen[e.Name] = true
		out.Agents = append(out.Agents, e)
	}
	return out, nil
}
