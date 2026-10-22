package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// fakeSkills serves files by "name/file", "" being SKILL.md.
type fakeSkills map[string]string

func (f fakeSkills) ReadSkill(_ context.Context, name, file string) ([]byte, error) {
	if file == "" {
		file = "SKILL.md"
	}
	text, ok := f[name+"/"+file]
	if !ok {
		return nil, errors.New("no such skill or file")
	}
	return []byte(text), nil
}

// TestSkillLoadIsReadOnlyAndTainted_REQ_SKL_005: skill_load is ReadOnly, so every mode offers
// it, and what it returns is untrusted content: the result taints the turn (REQ-SEC-006). A
// body past 256 KiB is cut the way read_file cuts, and without a store the tool refuses.
func TestSkillLoadIsReadOnlyAndTainted_REQ_SKL_005(t *testing.T) {
	big := strings.Repeat("x", maxReadBytes+100)
	tool := skillLoad{skills: fakeSkills{"release/SKILL.md": "# Release\nrun ./deploy.sh\n", "release/scripts/check.sh": "echo ok\n", "big/SKILL.md": big}}
	if spec := tool.Spec(); spec.Name != "skill_load" || spec.Risk != secdomain.RiskReadOnly {
		t.Fatalf("spec %+v", spec)
	}
	env := domain.Env{Cwd: t.TempDir()}
	action, err := tool.Action(env, in(map[string]any{"name": "release", "file": "scripts/check.sh"}))
	if err != nil || action.Risk != secdomain.RiskReadOnly || action.Tool != "skill_load" || action.Target != "release/scripts/check.sh" {
		t.Fatalf("action %+v %v", action, err)
	}
	res, err := tool.Run(t.Context(), env, in(map[string]any{"name": "release"}))
	if err != nil || !res.Tainted || !strings.Contains(res.Text, "run ./deploy.sh") {
		t.Fatalf("SKILL.md: %+v %v", res, err)
	}
	if res, err := tool.Run(t.Context(), env, in(map[string]any{"name": "release", "file": "scripts/check.sh"})); err != nil || !res.Tainted || res.Text != "echo ok\n" {
		t.Fatalf("a file: %+v %v", res, err)
	}
	res, err = tool.Run(t.Context(), env, in(map[string]any{"name": "big"}))
	if err != nil || !res.Tainted || len(res.Text) > maxReadBytes+200 || !strings.Contains(res.Text, "truncated") {
		t.Fatalf("a large body: %d bytes, tainted %v, %v", len(res.Text), res.Tainted, err)
	}
	if _, err := tool.Run(t.Context(), env, in(map[string]any{"name": "ghost"})); err == nil {
		t.Fatal("an unknown skill was served")
	}
	if _, err := (skillLoad{}).Run(t.Context(), env, in(map[string]any{"name": "release"})); err == nil {
		t.Fatal("skill_load ran without a store")
	}
}
