// Package ports is the ports layer of the context module: what the agent runtime reads a
// turn's context through, and the block text the module needs from sessions.
package ports

import (
	"context"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/domain"
)

// Gatherer reads the context of a thread's cwd from the machine.
type Gatherer interface {
	// Rules returns the rules files for cwd, highest precedence first (REQ-CTX-001).
	Rules(ctx context.Context, cwd string) ([]domain.RulesFile, error)
	// Git returns the cwd's git state, or nil when cwd is not inside a repository
	// (REQ-CTX-003).
	Git(ctx context.Context, cwd string) (*domain.GitContext, error)
	// Attach reads one attachment, relative to cwd (REQ-CTX-002, REQ-CTX-005). An attachment
	// that names nothing it can attach is domain.ErrUnknownAttachment.
	Attach(ctx context.Context, cwd string, ref domain.Ref) (domain.Attachment, error)
}

// Blocks is the block history, as far as an `@block` attachment needs it.
type Blocks interface {
	BlockText(ctx context.Context, id string) (domain.BlockText, error)
}

// KindSkillChanged is `skill.changed` (API §6, delta `2026-09-skills-cli`).
const KindSkillChanged bus.Kind = "skill.changed"

// SkillChanged announces a change to the installed skills, published once it is stored
// (DD-007). Change is one of domain.SkillInstalled … domain.SkillRemoved.
type SkillChanged struct {
	Name   string
	Change string
}

// EventKind implements bus.Event.
func (SkillChanged) EventKind() bus.Kind { return KindSkillChanged }

// Skills is the skill store as the API serves it, `skill.*` (REQ-SKL-001, REQ-SKL-002,
// REQ-SKL-006). Errors are domain.ErrSkillValidation, ErrSkillConflict and ErrSkillNotFound.
type Skills interface {
	// Inspect checks the bundle at path and returns its manifest, copying nothing.
	Inspect(ctx context.Context, path string) (domain.Manifest, error)
	// Install checks the bundle again and installs it if its digest is still the one
	// inspected; a 65th skill is stored disabled.
	Install(ctx context.Context, p domain.InstallParams) (domain.Skill, error)
	// List is every installed skill by name.
	List(ctx context.Context) ([]domain.Skill, error)
	// Get reads one skill, and SKILL.md's body after its front matter when withBody is set.
	Get(ctx context.Context, name string, withBody bool) (domain.Skill, string, error)
	// SetEnabled enables or disables a skill; a 65th enabled is refused.
	SetEnabled(ctx context.Context, name string, enabled bool) (domain.Skill, error)
	// Remove deletes the record, then the stored copy.
	Remove(ctx context.Context, name string) error
}
