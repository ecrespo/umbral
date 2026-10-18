package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// ErrInvalidRules is a rule file or bundle that cannot be used as a whole (REQ-SEC-010): it
// is ignored with a warning, never applied in part.
var ErrInvalidRules = errors.New("invalid rules")

// The bounds of a rule document. RE2 keeps every pattern linear in its input; these keep a
// document from costing the daemon more than a rule set is worth.
const (
	MaxRulesPerKind   = 256
	MaxRulePatternLen = 4096
	maxRuleNameLen    = 64
)

var ruleName = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// RuleSet is the material a rule file or bundle carries: the redaction rules and the
// destructive patterns (DD-016).
type RuleSet struct {
	Redaction   []RedactionRule
	Destructive []DestructivePattern
}

// RuleDocument is one parsed file or bundle. Version is a bundle's (DD-016: a positive
// integer, the built-in rules being 0); a local file may leave it out.
type RuleDocument struct {
	Version int64
	Rules   RuleSet
}

// BuiltinRules is the rule set compiled into the binary.
func BuiltinRules() RuleSet {
	return RuleSet{Redaction: DefaultRedactionRules, Destructive: DefaultDestructivePatterns}
}

// Policy is the engine over this set's destructive patterns.
func (s RuleSet) Policy() Policy { return Policy{Destructive: s.Destructive} }

// Redactor applies this set's redaction rules and the entropy detector.
func (s RuleSet) Redactor() Redactor { return Redactor{Rules: s.Redaction, Entropy: true} }

type wireRules struct {
	Version     int64             `json:"version"`
	Redaction   []wireRule        `json:"redaction"`
	Destructive []wireDestructive `json:"destructive"`
}

// wireDestructive has no group, so a destructive entry naming one is an unknown member.
type wireDestructive struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

type wireRule struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Group   int    `json:"group"`
}

// ParseRules reads a rule document:
//
//	{"version": 3,
//	 "redaction":   [{"name": "acme_token", "pattern": "…", "group": 1}],
//	 "destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}
//
// Every member is optional; an unknown one, a pattern that does not compile, a name outside
// `[a-z0-9_]{1,64}`, a group the pattern lacks or a name used twice in one kind refuses the
// whole document.
func ParseRules(data []byte) (RuleDocument, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc0 *wireRules
	if err := dec.Decode(&doc0); err != nil {
		return RuleDocument{}, fmt.Errorf("%w: %w", ErrInvalidRules, err)
	}
	if doc0 == nil {
		return RuleDocument{}, fmt.Errorf("%w: the document is null", ErrInvalidRules)
	}
	w := *doc0
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return RuleDocument{}, fmt.Errorf("%w: content after the document", ErrInvalidRules)
	}
	if w.Version < 0 {
		return RuleDocument{}, fmt.Errorf("%w: version %d is negative", ErrInvalidRules, w.Version)
	}
	doc := RuleDocument{Version: w.Version}
	var err error
	if doc.Rules.Redaction, err = compile(w.Redaction, "redaction", func(name string, expr *regexp.Regexp, group int) (RedactionRule, error) {
		if group < 0 || group > expr.NumSubexp() {
			return RedactionRule{}, fmt.Errorf("group %d, and the pattern has %d", group, expr.NumSubexp())
		}
		return RedactionRule{Name: name, Expr: expr, Group: group}, nil
	}); err != nil {
		return RuleDocument{}, err
	}
	destructive := make([]wireRule, len(w.Destructive))
	for i, d := range w.Destructive {
		destructive[i] = wireRule{Name: d.Name, Pattern: d.Pattern}
	}
	if doc.Rules.Destructive, err = compile(destructive, "destructive", func(name string, expr *regexp.Regexp, _ int) (DestructivePattern, error) {
		return DestructivePattern{Name: name, Expr: expr}, nil
	}); err != nil {
		return RuleDocument{}, err
	}
	return doc, nil
}

func compile[T any](rules []wireRule, kind string, build func(string, *regexp.Regexp, int) (T, error)) ([]T, error) {
	if len(rules) > MaxRulesPerKind {
		return nil, fmt.Errorf("%w: %d %s rules, at most %d", ErrInvalidRules, len(rules), kind, MaxRulesPerKind)
	}
	seen := map[string]bool{}
	out := make([]T, 0, len(rules))
	for i, r := range rules {
		if !ruleName.MatchString(r.Name) || len(r.Name) > maxRuleNameLen {
			return nil, fmt.Errorf("%w: %s rule %d: name %q is not 1 to 64 of [a-z0-9_]", ErrInvalidRules, kind, i, r.Name)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("%w: %s rule %q appears twice", ErrInvalidRules, kind, r.Name)
		}
		seen[r.Name] = true
		if r.Pattern == "" || len(r.Pattern) > MaxRulePatternLen {
			return nil, fmt.Errorf("%w: %s rule %q: the pattern must be 1 to %d bytes", ErrInvalidRules, kind, r.Name, MaxRulePatternLen)
		}
		expr, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %s rule %q: %w", ErrInvalidRules, kind, r.Name, err)
		}
		v, err := build(r.Name, expr, r.Group)
		if err != nil {
			return nil, fmt.Errorf("%w: %s rule %q: %w", ErrInvalidRules, kind, r.Name, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Layer puts overrides over a base set (REQ-SEC-010): a rule whose name the base has
// replaces it, a new name is added, and every override runs before what is left of the base,
// since a local rule is the more specific one. Of two overrides with one name the later
// wins. No override leaves the base as it was: there is never an empty set to fall back to.
func Layer(base RuleSet, overrides ...RuleSet) RuleSet {
	var red []RedactionRule
	var des []DestructivePattern
	for _, o := range overrides {
		red = replaceNamed(red, o.Redaction, func(r RedactionRule) string { return r.Name })
		des = replaceNamed(des, o.Destructive, func(p DestructivePattern) string { return p.Name })
	}
	return RuleSet{
		Redaction:   append(red, without(base.Redaction, red, func(r RedactionRule) string { return r.Name })...),
		Destructive: append(des, without(base.Destructive, des, func(p DestructivePattern) string { return p.Name })...),
	}
}

// replaceNamed adds each of more to have, replacing an entry of the same name where it is.
func replaceNamed[T any](have, more []T, name func(T) string) []T {
	for _, m := range more {
		replaced := false
		for i := range have {
			if name(have[i]) == name(m) {
				have[i], replaced = m, true
				break
			}
		}
		if !replaced {
			have = append(have, m)
		}
	}
	return have
}

// without is base less the names over holds.
func without[T any](base, over []T, name func(T) string) []T {
	taken := map[string]bool{}
	for _, o := range over {
		taken[name(o)] = true
	}
	out := make([]T, 0, len(base))
	for _, b := range base {
		if !taken[name(b)] {
			out = append(out, b)
		}
	}
	return out
}
