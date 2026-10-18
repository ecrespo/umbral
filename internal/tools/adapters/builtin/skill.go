package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
	"github.com/ecrespo/umbral/internal/tools/ports"
)

// skillLoad is skill_load: an installed skill's SKILL.md, or one file of its bundle
// (REQ-SKL-004). It reads only the stored copy, through the store, so it is ReadOnly; what it
// returns is third-party text, so every result taints the turn (REQ-SKL-005, REQ-SEC-006).
type skillLoad struct {
	skills ports.SkillReader
}

type skillInput struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func (skillLoad) Spec() domain.Spec {
	return domain.Spec{
		Name:        "skill_load",
		Description: "Read an installed skill listed under Skills: its SKILL.md, or one file of its bundle.",
		Risk:        secdomain.RiskReadOnly,
		InputSchema: object([]string{"name"}, map[string]any{
			"name": nonEmpty("The skill's name, as listed under Skills."),
			"file": str("A file of the skill's bundle, relative to it; SKILL.md by default."),
		}),
	}
}

func (skillLoad) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in skillInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return secdomain.Action{ThreadID: env.ThreadID, Tool: "skill_load", Risk: secdomain.RiskReadOnly, Target: skillTarget(in), Cwd: env.Cwd, WriteRoot: env.WriteRoot}, nil
}

func skillTarget(in skillInput) string {
	if in.File == "" {
		return in.Name
	}
	return in.Name + "/" + in.File
}

func (skillLoad) Summary(input json.RawMessage) string {
	var in skillInput
	_ = json.Unmarshal(input, &in)
	return "load skill " + skillTarget(in)
}

func (t skillLoad) Run(ctx context.Context, _ domain.Env, input json.RawMessage) (domain.Result, error) {
	if t.skills == nil {
		return domain.Result{}, errors.New("skill_load: no skill store")
	}
	var in skillInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	data, err := t.skills.ReadSkill(ctx, in.Name, in.File)
	if err != nil {
		return domain.Result{}, err
	}
	text := string(data)
	if len(text) > maxReadBytes {
		cut := maxReadBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + fmt.Sprintf("\n[truncated: %d bytes omitted]\n", len(data)-cut)
	}
	return domain.Result{Summary: "load skill " + skillTarget(in), Text: text, Tainted: true}, nil
}
