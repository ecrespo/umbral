package api

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	ctxports "github.com/ecrespo/umbral/internal/context/ports"
)

// fakeSkills answers fixed values and the store's sentinels: what the test sees is api's half.
type fakeSkills struct {
	installed []ctxdomain.InstallParams
}

var fakeSkill = ctxdomain.Skill{
	ID: "skl_1", Name: "release-check", Description: "Steps to cut a release.", Source: "/src/release-check",
	SHA256: "ab", SizeBytes: 42, Enabled: true, InstalledAt: 7,
}

func (f *fakeSkills) Inspect(_ context.Context, path string) (ctxdomain.Manifest, error) {
	if ctxdomain.IsRemoteSource(path) {
		return ctxdomain.Manifest{}, fmt.Errorf("%w: %q is not a local path", ctxdomain.ErrSkillValidation, path)
	}
	return ctxdomain.Manifest{
		Name: "release-check", Description: "Steps to cut a release.", Version: "1.2",
		Files: []ctxdomain.SkillFile{{Path: "SKILL.md", Size: 42}}, SizeBytes: 42, SHA256: "ab",
	}, nil
}

func (f *fakeSkills) Install(_ context.Context, p ctxdomain.InstallParams) (ctxdomain.Skill, error) {
	if p.ExpectedSHA256 != "ab" {
		return ctxdomain.Skill{}, fmt.Errorf("%w: the bundle changed", ctxdomain.ErrSkillConflict)
	}
	f.installed = append(f.installed, p)
	return fakeSkill, nil
}

func (f *fakeSkills) List(context.Context) ([]ctxdomain.Skill, error) {
	return []ctxdomain.Skill{fakeSkill}, nil
}

func (f *fakeSkills) Get(_ context.Context, name string, withBody bool) (ctxdomain.Skill, string, error) {
	if name != fakeSkill.Name {
		return ctxdomain.Skill{}, "", ctxdomain.ErrSkillNotFound
	}
	if withBody {
		return fakeSkill, "# Release\n", nil
	}
	return fakeSkill, "", nil
}

func (f *fakeSkills) SetEnabled(_ context.Context, _ string, enabled bool) (ctxdomain.Skill, error) {
	if enabled {
		return ctxdomain.Skill{}, fmt.Errorf("%w: 64 skills are enabled", ctxdomain.ErrSkillValidation)
	}
	s := fakeSkill
	s.Enabled = false
	return s, nil
}

func (f *fakeSkills) Remove(context.Context, string) error { return nil }

// TestSkillsOverTheWire_REQ_SKL_001: `umb` may call skill.*, `skills` is advertised, the
// results have API §5.42's shapes, the store's refusals map onto §3 — a remote source is
// VALIDATION_ERROR (REQ-SKL-006) and a changed digest CONFLICT (REQ-SKL-002) — and
// skill.changed reaches clients.
func TestSkillsOverTheWire_REQ_SKL_001(t *testing.T) {
	t.Parallel()
	svc := &fakeSkills{}
	b := bus.New()
	s := testServerWithConfig(t, Config{Skills: svc, Bus: b})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Notify(ctx)
	c := dial(t, s)
	hello := c.hello(s.Token(), ClientCLI)
	if hello.Error != nil {
		t.Fatalf("hello: %+v", hello.Error)
	}
	if !slices.Contains(capabilitiesOf(t, hello.Result), "skills") {
		t.Errorf("skills is not advertised: %v", capabilitiesOf(t, hello.Result))
	}
	skillJSON := `{"id":"skl_1","name":"release-check","description":"Steps to cut a release.","version":null,"source":"/src/release-check","sha256":"ab","size_bytes":42,"enabled":true,"installed_at":7}`

	resp := c.call(2, "skill.inspect", map[string]any{"path": "/src/release-check"})
	if got := string(ordered[skillManifest](t, resp.Result)); resp.Error != nil ||
		got != `{"name":"release-check","description":"Steps to cut a release.","version":"1.2","files":[{"path":"SKILL.md","size_bytes":42}],"size_bytes":42,"sha256":"ab"}` {
		t.Errorf("inspect = %s %+v", got, resp.Error)
	}
	resp = c.call(3, "skill.install", map[string]any{"path": "/src/release-check", "expected_sha256": "ab", "replace": true})
	if got := string(ordered[Skill](t, resp.Result)); resp.Error != nil || got != skillJSON || len(svc.installed) != 1 || !svc.installed[0].Replace {
		t.Errorf("install = %s %+v %+v", got, resp.Error, svc.installed)
	}
	resp = c.call(4, "skill.list", nil)
	if got := string(ordered[skillListResult](t, resp.Result)); resp.Error != nil || got != `{"items":[`+skillJSON+`]}` {
		t.Errorf("list = %s %+v", got, resp.Error)
	}
	resp = c.call(5, "skill.get", map[string]any{"name": "release-check", "include": "body"})
	if got := string(ordered[skillGetResult](t, resp.Result)); resp.Error != nil || got != `{"skill":`+skillJSON+`,"body":"# Release\n"}` {
		t.Errorf("get with the body = %s %+v", got, resp.Error)
	}
	resp = c.call(6, "skill.get", map[string]any{"name": "release-check"})
	if got := string(ordered[skillGetResult](t, resp.Result)); resp.Error != nil || got != `{"skill":`+skillJSON+`,"body":null}` {
		t.Errorf("get = %s %+v", got, resp.Error)
	}
	if resp = c.call(7, "skill.remove", map[string]any{"name": "release-check"}); resp.Error != nil {
		t.Errorf("remove: %+v", resp.Error)
	}

	for name, tc := range map[string]struct {
		method string
		params map[string]any
		code   string
	}{
		"a remote source":      {"skill.inspect", map[string]any{"path": "https://example.com/s.tar.gz"}, domainValidationError},
		"a changed digest":     {"skill.install", map[string]any{"path": "/src/x", "expected_sha256": "cd"}, domainConflict},
		"no digest":            {"skill.install", map[string]any{"path": "/src/x"}, domainValidationError},
		"no path":              {"skill.inspect", map[string]any{}, domainValidationError},
		"an unknown skill":     {"skill.get", map[string]any{"name": "nope"}, domainNotFound},
		"a bad include":        {"skill.get", map[string]any{"name": "release-check", "include": "all"}, domainValidationError},
		"a 65th enabled skill": {"skill.set_enabled", map[string]any{"name": "release-check", "enabled": true}, domainValidationError},
	} {
		resp := c.call(8, tc.method, tc.params)
		if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != tc.code {
			t.Errorf("%s: %+v, want %s", name, resp.Error, tc.code)
		}
	}

	b.Publish(ctxports.SkillChanged{Name: "release-check", Change: ctxdomain.SkillRemoved})
	method, _, n := c.readNotification(t)
	if method != "skill.changed" || string(n) != `{"name":"release-check","change":"removed"}` {
		t.Fatalf("skill.changed = %s %s", method, n)
	}
}

// TestSkillsAreNotImplementedWithoutTheStore: a daemon built without the skill store answers
// NOT_IMPLEMENTED and does not advertise `skills`.
func TestSkillsAreNotImplementedWithoutTheStore(t *testing.T) {
	t.Parallel()
	s := testServerWithConfig(t, Config{})
	c := dial(t, s)
	if hello := c.hello(s.Token(), ClientCLI); slices.Contains(capabilitiesOf(t, hello.Result), "skills") {
		t.Error("skills is advertised without the store")
	}
	if resp := c.call(2, "skill.list", nil); resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.DomainCode != "NOT_IMPLEMENTED" {
		t.Errorf("skill.list = %+v", resp.Error)
	}
}
