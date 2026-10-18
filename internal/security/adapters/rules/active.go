package rules

import (
	"sync"

	"github.com/ecrespo/umbral/internal/security/domain"
)

// Active is the rule set in force: a base — the built-in rules, or a verified bundle — with
// the local overrides always on top (REQ-SEC-010, REQ-SEC-011: a bundle never overrides the
// local directory). The agent runtime, policy.explain and every redaction read it, so all
// three apply the same rules.
type Active struct {
	mu      sync.RWMutex
	local   domain.RuleSet
	current domain.RuleSet
}

// NewActive starts from the built-in rules under the given local overrides.
func NewActive(local domain.RuleSet) *Active {
	return &Active{local: local, current: domain.Layer(domain.BuiltinRules(), local)}
}

// HasLocal reports whether the override directory contributed any rule (`rules.status`'s
// `local_override`).
func (a *Active) HasLocal() bool {
	return len(a.local.Redaction)+len(a.local.Destructive) > 0
}

// SetBase replaces the base — a verified bundle replaces the built-in rules whole, so a name it
// leaves out is gone — keeping the local overrides on top (DD-016).
func (a *Active) SetBase(base domain.RuleSet) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current = domain.Layer(base, a.local)
}

// Rules is the set in force.
func (a *Active) Rules() domain.RuleSet {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.current
}

// Policy is the engine over the destructive patterns in force.
func (a *Active) Policy() domain.Policy { return a.Rules().Policy() }

// Redact applies the redaction rules in force and the entropy detector.
func (a *Active) Redact(text string) domain.Redaction { return a.Rules().Redactor().Redact(text) }
