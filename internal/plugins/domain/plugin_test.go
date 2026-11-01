package domain

import (
	"errors"
	"strings"
	"testing"
)

func validManifest() (Manifest, map[string][]byte) {
	files := map[string][]byte{
		ManifestFile:              []byte("name = \"acme\"\n"),
		"skills/review/SKILL.md":  []byte("---\nname: acme-review\ndescription: Review things\n---\nBody\n"),
		"detect/acme.toml":        []byte("name = \"acme\"\n"),
		"skills/review/notes.txt": []byte("x"),
	}
	m := Manifest{
		Name: "acme", Version: "1.0.0", Description: "Acme tools",
		Commands:  []Command{{Name: "deploy", Argv: []string{"make", "deploy"}}},
		Servers:   []Server{{Name: "acme", Transport: "stdio", Command: "acme-mcp"}},
		Skills:    []Skill{{Path: "skills/review"}},
		Detection: []Detection{{Path: "detect/acme.toml"}},
		Keys:      []Key{{Key: "ctrl+alt+d", Command: "deploy"}},
	}
	return m, files
}

func okDetection([]byte) error { return nil }

// TestAManifestIsCheckedWhole_REQ_PLG_001: every contribution is checked at inspect, so an
// install never fails late on a malformed one (delta `2026-10-plugins`, decision 2).
func TestAManifestIsCheckedWhole_REQ_PLG_001(t *testing.T) {
	t.Parallel()
	m, files := validManifest()
	got, err := Check(m, files, okDetection)
	if err != nil {
		t.Fatalf("a valid manifest was refused: %v", err)
	}
	if got.Skills[0].Name != "acme-review" {
		t.Errorf("the skill's name = %q, want the one its SKILL.md gives", got.Skills[0].Name)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Manifest, map[string][]byte)
		want   string
	}{
		{"a bad name", func(m *Manifest, _ map[string][]byte) { m.Name = "Acme!" }, "name"},
		{"no version", func(m *Manifest, _ map[string][]byte) { m.Version = "" }, "version"},
		{"an empty argv", func(m *Manifest, _ map[string][]byte) { m.Commands[0].Argv = nil }, "argv"},
		{"a control character in argv", func(m *Manifest, _ map[string][]byte) { m.Commands[0].Argv = []string{"make", "x\ny"} }, "control"},
		{"a command twice", func(m *Manifest, _ map[string][]byte) { m.Commands = append(m.Commands, m.Commands[0]) }, "twice"},
		{"a server mcp.server.add refuses", func(m *Manifest, _ map[string][]byte) { m.Servers[0].Command = "" }, "mcp server"},
		{"a skill directory with no SKILL.md", func(m *Manifest, _ map[string][]byte) { m.Skills[0].Path = "detect" }, "skill"},
		{"a skill outside the bundle", func(m *Manifest, _ map[string][]byte) { m.Skills[0].Path = "../x" }, "skill"},
		{"a missing detection file", func(m *Manifest, _ map[string][]byte) { m.Detection[0].Path = "detect/none.toml" }, "detection"},
		{"a key outside ctrl+alt", func(m *Manifest, _ map[string][]byte) { m.Keys[0].Key = "ctrl+t" }, "key"},
		{"a key for no command", func(m *Manifest, _ map[string][]byte) { m.Keys[0].Command = "nothing" }, "not one of"},
		{"a key bound twice", func(m *Manifest, _ map[string][]byte) { m.Keys = append(m.Keys, m.Keys[0]) }, "twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, files := validManifest()
			tc.mutate(&m, files)
			_, err := Check(m, files, okDetection)
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Check = %v, want a validation error naming %q", err, tc.want)
			}
		})
	}

	m, files = validManifest()
	if _, err := Check(m, files, func([]byte) error { return errors.New("unknown key") }); !errors.Is(err, ErrValidation) {
		t.Errorf("a detection manifest its parser refuses passed: %v", err)
	}
}

// TestAServerIsAlwaysAddedUntrusted_REQ_PLG_002: a plugin's server reaches mcp.server.add as
// untrusted, whatever else it declares (decision 5).
func TestAServerIsAlwaysAddedUntrusted_REQ_PLG_002(t *testing.T) {
	t.Parallel()
	m, _ := validManifest()
	if got := m.Servers[0].AddParams().Trust; got != "untrusted" {
		t.Errorf("trust = %q, want untrusted", got)
	}
}
