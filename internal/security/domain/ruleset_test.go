package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRulesAcceptsAWellFormedFile_REQ_SEC_010(t *testing.T) {
	t.Parallel()
	doc, err := ParseRules([]byte(`{"version": 3,
		"redaction": [{"name": "acme_token", "pattern": "\\bacme_[A-Za-z0-9]{32}\\b"},
		              {"name": "acme_secret", "pattern": "ACME_SECRET=(\\S+)", "group": 1}],
		"destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}`))
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if doc.Version != 3 || len(doc.Rules.Redaction) != 2 || len(doc.Rules.Destructive) != 1 || doc.Rules.Redaction[1].Group != 1 {
		t.Fatalf("parsed %+v", doc)
	}
}

func TestParseRulesRefusesWhatItCannotTrust_REQ_SEC_010(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not JSON":            `{"redaction": [`,
		"an unknown member":   `{"redactions": []}`,
		"a bad pattern":       `{"redaction": [{"name": "x", "pattern": "("}]}`,
		"no name":             `{"redaction": [{"pattern": "x"}]}`,
		"a name out of shape": `{"redaction": [{"name": "Bad Name", "pattern": "x"}]}`,
		"an empty pattern":    `{"destructive": [{"name": "x", "pattern": ""}]}`,
		"a group too high":    `{"redaction": [{"name": "x", "pattern": "a(b)", "group": 2}]}`,
		"a duplicate name":    `{"destructive": [{"name": "x", "pattern": "a"}, {"name": "x", "pattern": "b"}]}`,
		"a negative version":  `{"version": -1, "redaction": []}`,
		"a pattern too long":  `{"redaction": [{"name": "x", "pattern": "` + strings.Repeat("a", MaxRulePatternLen+1) + `"}]}`,
		"trailing content":    `{"redaction": []} {}`,
		"a destructive group": `{"destructive": [{"name": "x", "pattern": "a", "group": 0}]}`,
		"a null document":     `null`,
		// The trace's names for a line the floor cannot read (delta `2026-10-shell-parser`).
		"a reserved name":  `{"destructive": [{"name": "dynamic_word", "pattern": "a"}]}`,
		"another reserved": `{"destructive": [{"name": "stdin_script", "pattern": "a"}]}`,
		"a third reserved": `{"destructive": [{"name": "unparseable", "pattern": "a"}]}`,
	} {
		if _, err := ParseRules([]byte(body)); !errors.Is(err, ErrInvalidRules) {
			t.Errorf("%s: %v, want ErrInvalidRules", name, err)
		}
	}
}

// TestLocalRulesWinOverTheBase_REQ_SEC_010: an override with a base rule's name replaces it,
// a new name is added, local rules run first, and no override leaves the base as it was —
// never an empty set.
func TestLocalRulesWinOverTheBase_REQ_SEC_010(t *testing.T) {
	t.Parallel()
	base := BuiltinRules()
	if got := Layer(base); len(got.Redaction) != len(base.Redaction) || len(got.Destructive) != len(base.Destructive) {
		t.Fatalf("no override changed the base: %d/%d rules", len(got.Redaction), len(got.Destructive))
	}
	first, err := ParseRules([]byte(`{"redaction": [{"name": "acme_token", "pattern": "acme_[a-z]{8}"}],
		"destructive": [{"name": "rm_recursive", "pattern": "^rm( |$)"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseRules([]byte(`{"redaction": [{"name": "acme_token", "pattern": "acme_[0-9]{8}"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := Layer(base, first.Rules, second.Rules)
	if got.Redaction[0].Name != "acme_token" || got.Redaction[0].Expr.String() != "acme_[0-9]{8}" {
		t.Errorf("the later file's rule should run first: %s %s", got.Redaction[0].Name, got.Redaction[0].Expr)
	}
	if len(got.Redaction) != len(base.Redaction)+1 {
		t.Errorf("%d redaction rules, want the base plus one", len(got.Redaction))
	}
	if name, ok := MatchDestructive(got.Destructive, "rm notes.txt"); !ok || name != "rm_recursive" {
		t.Errorf("the overridden rm_recursive does not apply: %q %v", name, ok)
	}
	n := 0
	for _, p := range got.Destructive {
		if p.Name == "rm_recursive" {
			n++
		}
	}
	if n != 1 || len(got.Destructive) != len(base.Destructive) {
		t.Errorf("rm_recursive appears %d times among %d patterns; want it replaced, not added", n, len(got.Destructive))
	}
	if r := (Redactor{Rules: got.Redaction}).Redact("token acme_12345678 here"); !strings.Contains(r.Text, "[REDACTED:acme_token]") {
		t.Errorf("the local redaction rule did not apply: %q", r.Text)
	}
}
