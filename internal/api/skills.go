package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	ctxports "github.com/ecrespo/umbral/internal/context/ports"
)

// fieldExpectedSHA256 is the digest a write or an install checks against (§5.25, §5.42, §5.45).
const fieldExpectedSHA256 = "expected_sha256"

// Skill is API Spec §4's Skill (delta `2026-09-skills-cli`).
type Skill struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     *string `json:"version"`
	Source      string  `json:"source"`
	SHA256      string  `json:"sha256"`
	SizeBytes   int64   `json:"size_bytes"`
	Enabled     bool    `json:"enabled"`
	InstalledAt int64   `json:"installed_at"`
}

func toWireSkill(s ctxdomain.Skill) Skill {
	return Skill{
		ID: s.ID, Name: s.Name, Description: s.Description, Version: emptyAsNull(s.Version), Source: s.Source,
		SHA256: s.SHA256, SizeBytes: s.SizeBytes, Enabled: s.Enabled, InstalledAt: s.InstalledAt,
	}
}

// skillChangedPayload is `skill.changed` (API §6).
type skillChangedPayload struct {
	Name   string `json:"name"`
	Change string `json:"change"`
}

func hasSkills(cfg Config) bool { return cfg.Skills != nil }

func skillStore(c *conn) (ctxports.Skills, error) {
	if c.server.cfg.Skills == nil {
		return nil, fmt.Errorf("%w: the skill store is not wired in", ErrNotImplemented)
	}
	return c.server.cfg.Skills, nil
}

// skillMethods is §5.42. Every client kind may call them: `umb skill` is how skills are
// installed (decision 7).
func skillMethods() map[string]method {
	return map[string]method{
		"skill.inspect":     {handle: handleSkillInspect, kinds: treeClients, params: skillPathParams{}, result: skillManifest{}},
		"skill.install":     {handle: handleSkillInstall, userOnly: true, kinds: treeClients, params: skillInstallParams{}, result: Skill{}},
		"skill.list":        {handle: handleSkillList, kinds: treeClients, params: emptyResult{}, result: skillListResult{}},
		"skill.get":         {handle: handleSkillGet, kinds: treeClients, params: skillGetParams{}, result: skillGetResult{}},
		"skill.set_enabled": {handle: handleSkillSetEnabled, userOnly: true, kinds: treeClients, params: skillSetEnabledParams{}, result: Skill{}},
		"skill.remove":      {handle: handleSkillRemove, userOnly: true, kinds: treeClients, params: skillNameParams{}, result: emptyResult{}},
	}
}

type skillPathParams struct {
	Path string `json:"path"`
}

type skillFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

type skillManifest struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Version     *string     `json:"version"`
	Files       []skillFile `json:"files"`
	SizeBytes   int64       `json:"size_bytes"`
	SHA256      string      `json:"sha256"`
}

func requirePath(path string) error {
	if path == "" {
		return ValidationError("path is required", ErrorField{Field: "path", Issue: requiredTag})
	}
	return nil
}

func handleSkillInspect(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := skillStore(c)
	if err != nil {
		return nil, err
	}
	var p skillPathParams
	if err := decode(raw, &p, "skill.inspect"); err != nil {
		return nil, err
	}
	if err := requirePath(p.Path); err != nil {
		return nil, err
	}
	m, err := svc.Inspect(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	out := skillManifest{Name: m.Name, Description: m.Description, Version: emptyAsNull(m.Version), SizeBytes: m.SizeBytes, SHA256: m.SHA256, Files: make([]skillFile, 0, len(m.Files))}
	for _, f := range m.Files {
		out.Files = append(out.Files, skillFile{Path: f.Path, SizeBytes: f.Size})
	}
	return out, nil
}

type skillInstallParams struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Replace        bool   `json:"replace" api:"optional"`
}

func handleSkillInstall(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := skillStore(c)
	if err != nil {
		return nil, err
	}
	var p skillInstallParams
	if err := decode(raw, &p, "skill.install"); err != nil {
		return nil, err
	}
	if err := requirePath(p.Path); err != nil {
		return nil, err
	}
	if p.ExpectedSHA256 == "" {
		return nil, ValidationError("expected_sha256 is required: install exactly what skill.inspect showed",
			ErrorField{Field: fieldExpectedSHA256, Issue: requiredTag})
	}
	sk, err := svc.Install(ctx, ctxdomain.InstallParams{Path: p.Path, ExpectedSHA256: p.ExpectedSHA256, Replace: p.Replace})
	if err != nil {
		return nil, err
	}
	return toWireSkill(sk), nil
}

type skillListResult struct {
	Items []Skill `json:"items"`
}

func handleSkillList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := skillStore(c)
	if err != nil {
		return nil, err
	}
	if err := decode(raw, &emptyResult{}, "skill.list"); err != nil {
		return nil, err
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := skillListResult{Items: make([]Skill, 0, len(list))}
	for _, s := range list {
		out.Items = append(out.Items, toWireSkill(s))
	}
	return out, nil
}

type skillNameParams struct {
	Name string `json:"name"`
}

func requireName(name string) error {
	if name == "" {
		return ValidationError("name is required", ErrorField{Field: "name", Issue: requiredTag})
	}
	return nil
}

// includeBody is skill.get's `include` asking for SKILL.md's body.
const includeBody = "body"

type skillGetParams struct {
	Name    string `json:"name"`
	Include string `json:"include" api:"optional"`
}

type skillGetResult struct {
	Skill Skill   `json:"skill"`
	Body  *string `json:"body"`
}

func handleSkillGet(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := skillStore(c)
	if err != nil {
		return nil, err
	}
	var p skillGetParams
	if err := decode(raw, &p, "skill.get"); err != nil {
		return nil, err
	}
	if err := requireName(p.Name); err != nil {
		return nil, err
	}
	if p.Include != "" && p.Include != "none" && p.Include != includeBody {
		return nil, ValidationError(fmt.Sprintf("include is %q, want none or body", p.Include),
			ErrorField{Field: "include", Issue: "must be none or body"})
	}
	sk, body, err := svc.Get(ctx, p.Name, p.Include == includeBody)
	if err != nil {
		return nil, err
	}
	out := skillGetResult{Skill: toWireSkill(sk)}
	if p.Include == includeBody {
		out.Body = &body
	}
	return out, nil
}

type skillSetEnabledParams struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func handleSkillSetEnabled(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := skillStore(c)
	if err != nil {
		return nil, err
	}
	var p skillSetEnabledParams
	if err := decode(raw, &p, "skill.set_enabled"); err != nil {
		return nil, err
	}
	if err := requireName(p.Name); err != nil {
		return nil, err
	}
	sk, err := svc.SetEnabled(ctx, p.Name, p.Enabled)
	if err != nil {
		return nil, err
	}
	return toWireSkill(sk), nil
}

func handleSkillRemove(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := skillStore(c)
	if err != nil {
		return nil, err
	}
	var p skillNameParams
	if err := decode(raw, &p, "skill.remove"); err != nil {
		return nil, err
	}
	if err := requireName(p.Name); err != nil {
		return nil, err
	}
	if err := svc.Remove(ctx, p.Name); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

// skillsDomainError maps the skill store's sentinels onto §3.
func skillsDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, ctxdomain.ErrSkillValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, ctxdomain.ErrSkillConflict):
		return codeConflict, domainConflict, true
	case errors.Is(err, ctxdomain.ErrSkillNotFound):
		return codeNotFound, domainNotFound, true
	default:
		return 0, "", false
	}
}
