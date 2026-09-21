package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPaneHistoryDisabledByDefault_REQ_TERM_010 is the requirement's own default.
//
// "The setting SHALL be disabled by default because pane output can contain secrets" — so a
// machine with no settings file captures nothing, and this is the case almost every user is
// in. It is asserted rather than assumed because the failure is invisible: screens would be
// written to SQLite and nobody would be told.
func TestPaneHistoryDisabledByDefault_REQ_TERM_010(t *testing.T) {
	t.Parallel()

	settings, err := LoadSettings(nil, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("an absent settings file must not be an error: %v", err)
	}
	if settings.PaneHistory {
		t.Error("pane history is on with no settings file; REQ-TERM-010 makes it opt-in " +
			"because pane output can contain secrets")
	}

	// And an empty file, which is what a user left after deleting a setting.
	path := write(t, "")
	settings, err = LoadSettings(nil, path)
	if err != nil {
		t.Fatalf("an empty settings file: %v", err)
	}
	if settings.PaneHistory {
		t.Error("pane history is on with an empty settings file")
	}
}

// TestPaneHistoryTurnsOn_REQ_TERM_010 is the other half: asking for it works.
func TestPaneHistoryTurnsOn_REQ_TERM_010(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"[experimental]\npane_history = true\n",
		"# a comment\n\n[experimental]\npane_history = true   # inline\n",
	} {
		settings, err := LoadSettings(nil, write(t, body))
		if err != nil {
			t.Fatalf("load %q: %v", body, err)
		}
		if !settings.PaneHistory {
			t.Errorf("pane_history stayed off for:\n%s", body)
		}
	}
}

// TestMalformedConfigRefusesToStart is the decision that matters most in this file.
//
// A parser that skips what it cannot read turns "pane_history = true" into false without
// telling anyone, and the user believes their screens are captured when they are not. So
// anything outside the subset is an error, and the error names the line.
func TestMalformedConfigRefusesToStart(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"unterminated section": "[experimental\npane_history = true\n",
		"empty section":        "[]\npane_history = true\n",
		"bare word":            "[experimental]\npane_history\n",
		"no key":               "[experimental]\n = true\n",
		"no value":             "[experimental]\npane_history =\n",
		"not a boolean":        "[experimental]\npane_history = yes please\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			settings, err := LoadSettings(nil, write(t, body))
			if err == nil {
				t.Fatalf("a malformed file was accepted and read as %+v; the daemon would "+
					"start with defaults the user did not ask for", settings)
			}
			if settings.PaneHistory {
				t.Error("a rejected file still set pane history on")
			}
		})
	}
}

// TestUnknownSettingIsNotFatal keeps a file written for a later version usable by this one.
func TestUnknownSettingIsNotFatal(t *testing.T) {
	t.Parallel()

	settings, err := LoadSettings(nil,
		write(t, "[experimental]\npane_history = true\nsomething_from_f2 = 7\n\n[future]\nx = 1\n"))
	if err != nil {
		t.Fatalf("an unknown key stopped the daemon: %v", err)
	}
	if !settings.PaneHistory {
		t.Error("an unknown key beside a known one swallowed the known one")
	}
}

// TestSettingsPathFollowsXDG pins where a user is meant to put the file.
func TestSettingsPathFollowsXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path, err := SettingsPath()
	if err != nil {
		t.Fatalf("SettingsPath: %v", err)
	}
	if want := filepath.Join(dir, "umbral", "config.toml"); path != want {
		t.Errorf("SettingsPath = %q, want %q", path, want)
	}
	if !strings.HasSuffix(path, SettingsFileName) {
		t.Errorf("SettingsPath = %q, want it to end in %q", path, SettingsFileName)
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the settings file: %v", err)
	}
	return path
}
