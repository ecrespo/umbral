package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Settings is what `$XDG_CONFIG_HOME/umbral/config.toml` can change about a daemon run
// (Tech Design §5.1, "Configuration").
//
// F0 reads one key. The type exists as a struct rather than a bool so that adding the second
// one is a field and not a signature change through every caller.
type Settings struct {
	// PaneHistory turns on REQ-TERM-010's capture and replay of pane screens. Off unless
	// the file says otherwise, and off is the default the requirement itself states:
	// pane output can contain secrets, so this is a thing a user opts into knowingly.
	PaneHistory bool
	// MaxMessageBytes is the frame limit a connection gets once it has completed the
	// handshake (`[api] max_message_bytes`, API Spec §1 and §8). DefaultMaxMessageBytes
	// unless the file says otherwise. It is the first live key: `limits.set` rewrites it and
	// the daemon applies it to new connections without a restart.
	MaxMessageBytes int
	// AllowEnvSecrets is `[secrets] allow_env`: whether `env:<VAR>` may stand in for a
	// keyring the machine does not have (REQ-SEC-012). Off by default, since the environment
	// is the weaker store — every child process inherits it.
	AllowEnvSecrets bool
	// OTelEndpoint is `[otel] endpoint`: the base URL of an OTLP/HTTP collector on this
	// machine that traces and metrics are exported to (REQ-OBS-003). Empty, the default,
	// exports nothing: there is no telemetry the user has not asked for (Art. 4).
	OTelEndpoint string
	// Retention is `[retention]`: Data Model §4's configurable windows, which the daily
	// maintenance job applies (T-F1-22).
	Retention Retention
}

// Retention is Data Model §4's configurable retention windows, in days.
type Retention struct {
	RawOutputDays       int // `raw_output_days`: a closed block's raw chunks
	PlainOutputDays     int // `plain_output_days`: a closed block's transcript
	ClosedStructureDays int // `closed_structure_days`: closed workspaces, tabs and panes
	AuditDays           int // `audit_days`: egress_log and usage
}

// DefaultRetention is Data Model §4's defaults.
func DefaultRetention() Retention {
	return Retention{RawOutputDays: 30, PlainOutputDays: 180, ClosedStructureDays: 30, AuditDays: 365}
}

// The bounds of a retention window, in days: at least one, at most ten years.
const (
	MinRetentionDays = 1
	MaxRetentionDays = 3650
)

// SettingsFileName is the file's name inside the configuration directory.
const SettingsFileName = "config.toml"

// Dir is where Umbral keeps files a user edits, as opposed to the runtime directory, which
// holds files only the daemon writes.
func Dir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "umbral"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate the home directory: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Umbral"), nil
	}
	return filepath.Join(home, ".config", "umbral"), nil
}

// SettingsPath is the settings file's full path.
func SettingsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SettingsFileName), nil
}

// LoadSettings reads the settings file.
//
// **An absent file is the default configuration and not an error.** Umbral runs with no
// configuration at all; a local-first tool owes a new user a working daemon before it owes
// them a settings file.
//
// **A malformed file stops the daemon.** Falling back to defaults after a user has asked for
// something is how `pane_history = true` silently becomes false, and someone believes their
// screens are being captured when they are not. That is the same reasoning REQ-SEC-010
// applies to rule files — "SHALL NOT fall back to an empty rule set" — pointed the other way:
// once a user has stated an intention, guessing is worse than stopping.
//
// An unknown key is a warning, so a file written for a later version still starts this one.
func LoadSettings(logger *slog.Logger, path string) (Settings, error) {
	settings := Settings{MaxMessageBytes: DefaultMaxMessageBytes, Retention: DefaultRetention()}

	raw, err := os.ReadFile(path) // #nosec G304 -- the path is the daemon's own config location
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, fmt.Errorf("config: read %s: %w", path, err)
	}

	values, err := parseTOMLSubset(string(raw))
	if err != nil {
		return settings, fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, path, err)
	}

	for key, value := range values {
		switch key {
		case "experimental.pane_history":
			on, err := strconv.ParseBool(value)
			if err != nil {
				return Settings{}, fmt.Errorf(
					"%w: %s: experimental.pane_history is %q, want true or false",
					ErrSettingsInvalid, path, value)
			}
			settings.PaneHistory = on
		case "secrets.allow_env":
			on, err := strconv.ParseBool(value)
			if err != nil {
				return Settings{}, fmt.Errorf("%w: %s: secrets.allow_env is %q, want true or false",
					ErrSettingsInvalid, path, value)
			}
			settings.AllowEnvSecrets = on
		case "otel.endpoint":
			endpoint, err := parseOTelEndpoint(value)
			if err != nil {
				return Settings{}, fmt.Errorf("%w: %s: otel.endpoint: %w", ErrSettingsInvalid, path, err)
			}
			settings.OTelEndpoint = endpoint
		case "retention.raw_output_days", "retention.plain_output_days",
			"retention.closed_structure_days", "retention.audit_days":
			days, err := strconv.Atoi(value)
			if err != nil || days < MinRetentionDays || days > MaxRetentionDays {
				return Settings{}, fmt.Errorf("%w: %s: %s is %q, want whole days from %d to %d",
					ErrSettingsInvalid, path, key, value, MinRetentionDays, MaxRetentionDays)
			}
			switch key {
			case "retention.raw_output_days":
				settings.Retention.RawOutputDays = days
			case "retention.plain_output_days":
				settings.Retention.PlainOutputDays = days
			case "retention.closed_structure_days":
				settings.Retention.ClosedStructureDays = days
			default:
				settings.Retention.AuditDays = days
			}
		case apiSection + "." + maxMessageKey:
			n, err := parseMaxMessageBytes(value)
			if err != nil {
				return Settings{}, fmt.Errorf("%w: %s: %s.%s: %w",
					ErrSettingsInvalid, path, apiSection, maxMessageKey, err)
			}
			settings.MaxMessageBytes = n
		default:
			if logger != nil {
				logger.Warn("unknown setting ignored",
					slog.String("key", key), slog.String("file", path))
			}
		}
	}
	return settings, nil
}

// parseOTelEndpoint checks `[otel] endpoint`: an http or https base URL — no path, query,
// fragment or credentials — whose host is `localhost` or a loopback address. A collector
// elsewhere would make the telemetry an egress Art. 4 requires to be recorded and redacted;
// F1 exports to a local collector only, which forwards wherever the user configures it to.
// The error never repeats the value, which may hold a credential.
func parseOTelEndpoint(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("want an http:// or https:// URL such as http://127.0.0.1:4318")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", errors.New("want the collector's base URL, with no path, query or credentials")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", errors.New("the collector must be on this machine: localhost or a loopback address")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// parseTOMLSubset reads the part of TOML this file uses: comments, `[section]` headers and
// `key = value` lines, returning `section.key` → the value's literal text.
//
// Not a TOML library, and deliberately not: F0 reads one boolean, and a dependency for that
// would be carried by everything that links the daemon. What it must not do is accept a file
// it has not understood — a parser that skips what it cannot read is the silent-fallback this
// function exists to avoid — so anything outside the subset is an error naming its line.
func parseTOMLSubset(text string) (map[string]string, error) {
	values := map[string]string{}
	section := ""

	for n, line := range strings.Split(text, "\n") {
		lineNo := n + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasPrefix(trimmed, "[") {
			if !strings.HasSuffix(trimmed, "]") {
				return nil, fmt.Errorf("line %d: %q is not a [section] header", lineNo, trimmed)
			}
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if section == "" {
				return nil, fmt.Errorf("line %d: an empty section name", lineNo)
			}
			continue
		}

		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			return nil, fmt.Errorf("line %d: %q is neither a section nor a key = value", lineNo, trimmed)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: a value with no key", lineNo)
		}

		// A quoted value ends at its closing quote, and only a comment may follow it; an
		// unquoted one ends at an inline comment. `[api] max_message_bytes = "8MiB"  # why`
		// is why the difference matters: reading past the quote made the comment part of
		// the size. Escapes inside quotes are not part of the subset.
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			end := strings.Index(value[1:], `"`)
			if end < 0 {
				return nil, fmt.Errorf("line %d: %q has an unterminated string", lineNo, key)
			}
			if rest := strings.TrimSpace(value[end+2:]); rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, fmt.Errorf("line %d: %q has text after its value", lineNo, key)
			}
			value = value[1 : end+1]
		} else if i := strings.Index(value, "#"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		// Inline tables and arrays are outside the subset. Read as a plain value, an
		// `api = { max_message_bytes = … }` became an unknown key `api` and the daemon
		// started with the default, which is the silent fallback this parser refuses.
		if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
			return nil, fmt.Errorf("line %d: %q is an inline table or array, which this file "+
				"does not support; write it as a [section] with one key per line", lineNo, key)
		}
		if value == "" {
			return nil, fmt.Errorf("line %d: %q has no value", lineNo, key)
		}

		if section != "" {
			key = section + "." + key
		}
		values[key] = value
	}
	return values, nil
}
