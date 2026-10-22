package domain

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// RedactionRule is one secret pattern. The match — or, when Group is set, that capture
// group only, so `KEY=` stays readable — is replaced by `[REDACTED:<Name>]`.
type RedactionRule struct {
	Name  string
	Expr  *regexp.Regexp
	Group int
}

// RuleHighEntropy names the generic detector's replacements.
const RuleHighEntropy = "high_entropy"

// Entropy thresholds of REQ-SEC-001: the generic detector only fires at or above both.
const (
	entropyMinBits   = 4.5
	entropyMinLength = 20
)

// DefaultRedactionRules is the list built into the binary; a rule bundle replaces it
// (REQ-SEC-010, T-F1-29). Order matters: a specific rule runs before a general one that
// would also match (Anthropic and OpenRouter before OpenAI's `sk-`, every named key before
// the `.env` rule), and a match that is already only placeholders is left alone.
var DefaultRedactionRules = []RedactionRule{
	{Name: "pem_private_key", Expr: regexp.MustCompile(
		`-----BEGIN[ A-Z0-9]*PRIVATE KEY( BLOCK)?-----[\s\S]*?(-----END[ A-Z0-9]*PRIVATE KEY( BLOCK)?-----|\z)`)},
	{Name: "anthropic_key", Expr: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{Name: "openrouter_key", Expr: regexp.MustCompile(`\bsk-or-v1-[A-Za-z0-9]{20,}`)},
	{Name: "openai_key", Expr: regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{20,}`)},
	{Name: "github_token", Expr: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})`)},
	{Name: "gitlab_token", Expr: regexp.MustCompile(`\bgl(?:pat|dt|rt|ptt|cbt)-[A-Za-z0-9_\-]{20,}`)},
	{Name: "huggingface_token", Expr: regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`)},
	{Name: "aws_access_key_id", Expr: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`)},
	{Name: "aws_secret_access_key", Group: 1, Expr: regexp.MustCompile(
		`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40,})`)},
	{Name: "gcp_api_key", Expr: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`)},
	{Name: "gcp_private_key_id", Group: 1, Expr: regexp.MustCompile(`"private_key_id"\s*:\s*"([a-f0-9]{40})"`)},
	{Name: "jwt", Expr: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{10,}`)},
	{Name: "slack_token", Expr: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	// A name is secret when one of its `_`-separated segments is a keyword, so MAX_TOKENS,
	// BYPASS_CACHE and GIT_AUTHOR_NAME are not; after the keyword only segments that keep it a
	// secret may follow (SECRET_KEY_BASE, API_KEY_2), so SSH_KEY_PATH and TOKEN_TTL are not.
	{Name: "dotenv_secret", Group: 1, Expr: regexp.MustCompile(
		`(?m)^[ \t]*(?:export[ \t]+)?(?:[A-Z0-9]+_)*(?:SECRETS?|TOKEN|PASSWORD|PASSWD|PASS|PWD|KEY|APIKEY|ACCESSKEY|CREDENTIALS?|AUTH)` +
			`(?:_(?:KEY|SECRET|TOKEN|BASE|VALUE|DATA|[0-9]+))*[ \t]*=[ \t]*` +
			`(?:"([^"\n]{6,})"|'([^'\n]{6,})'|([^\s#"']{6,}))`)},
}

// Redaction is Redact's result: the text to send, and how many matches each rule replaced,
// for the egress audit (DD-008).
type Redaction struct {
	Text   string
	Counts map[string]int
}

// Redactor applies a rule set and, when Entropy is on, the generic detector.
type Redactor struct {
	Rules   []RedactionRule
	Entropy bool
}

// Redact applies the built-in rules and the entropy detector (REQ-SEC-001).
func Redact(text string) Redaction {
	return Redactor{Rules: DefaultRedactionRules, Entropy: true}.Redact(text)
}

const placeholder = "[REDACTED:"

// Redact replaces every match, rule by rule, then every high-entropy token that remains.
func (r Redactor) Redact(text string) Redaction {
	out := Redaction{Text: text, Counts: map[string]int{}}
	for _, rule := range r.Rules {
		out.Text = replaceRule(out.Text, rule, out.Counts)
	}
	if r.Entropy {
		out.Text = candidates.ReplaceAllStringFunc(out.Text, func(tok string) string {
			if !highEntropy(tok) {
				return tok
			}
			out.Counts[RuleHighEntropy]++
			return placeholder + RuleHighEntropy + "]"
		})
	}
	return out
}

// replaceRule replaces the whole match, or with a Group the first non-empty group from it on
// (alternatives such as the three quotings of `.env` each capture their own group).
func replaceRule(text string, rule RedactionRule, counts map[string]int) string {
	matches := rule.Expr.FindAllStringSubmatchIndex(text, -1)
	if matches == nil {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if rule.Group > 0 {
			start = -1
			for g := rule.Group; 2*g+1 < len(m); g++ {
				if m[2*g] >= 0 {
					start, end = m[2*g], m[2*g+1]
					break
				}
			}
			if start < 0 {
				continue
			}
		}
		// Only a match that is nothing but placeholders is already redacted; anything else is
		// replaced whole, placeholders it holds included, so a password around a key does
		// not survive and a literal "[REDACTED:" in the input exempts nothing.
		found := placeholders.FindAllStringSubmatch(text[start:end], -1)
		if onlyPlaceholders.MatchString(text[start:end]) {
			continue
		}
		for _, f := range found {
			if counts[f[1]] > 0 {
				counts[f[1]]--
				if counts[f[1]] == 0 {
					delete(counts, f[1])
				}
			}
		}
		b.WriteString(text[last:start])
		b.WriteString(placeholder + rule.Name + "]")
		counts[rule.Name]++
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

var (
	placeholders     = regexp.MustCompile(`\[REDACTED:([a-z0-9_]+)\]`)
	onlyPlaceholders = regexp.MustCompile(`^(?:\[REDACTED:[a-z0-9_]+\])+$`)
)

// candidates are the tokens the entropy detector looks at: runs of the characters keys and
// their encodings are written in.
var candidates = regexp.MustCompile(`[A-Za-z0-9+/=_\-]{20,}`)

// umbralID is a type-prefixed ULID (`thr_…`, `blk_…`): random by construction, and never a
// secret — the model needs them to address threads and blocks.
var umbralID = regexp.MustCompile(`^[a-z]{2,5}_[0-9A-HJKMNP-TV-Z]{26}$`)

// highEntropy is REQ-SEC-001's generic detector. Its two thresholds are necessary, not
// sufficient ("only fires on"): an identifier made of words (`TestPolicyAutoEdit_REQ_AGT_013`)
// or an Umbral id can cross 4.5 bits and is still not a secret.
func highEntropy(tok string) bool {
	if len(tok) < entropyMinLength || shannon(tok) < entropyMinBits {
		return false
	}
	return !umbralID.MatchString(tok) && !wordLike(tok)
}

// shannon is the entropy of s in bits per character, over its own character frequencies.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]int{}
	n := 0
	for _, r := range s {
		freq[r]++
		n++
	}
	h := 0.0
	for _, c := range freq {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// wordLike reports whether a token is built like an identifier, a path or a test name:
// split at its separators, case changes and letter–digit boundaries, at most a tenth of its
// characters fall in pieces shorter than three (the `v2` of `config_v2`). Random keys
// break into one- and two-character pieces almost everywhere.
func wordLike(tok string) bool {
	runes := []rune(tok)
	short, total, piece := 0, 0, 0
	flush := func() {
		if piece > 0 && piece < 3 {
			short += piece
		}
		total += piece
		piece = 0
	}
	for i, r := range runes {
		if strings.ContainsRune("+/=_-", r) {
			flush()
			continue
		}
		if i > 0 && piece > 0 && boundary(runes, i) {
			flush()
		}
		piece++
	}
	flush()
	return total > 0 && short*10 <= total
}

// boundary is a split point inside a run: lower to upper (`fooBar`), and letter to digit or
// back. An acronym stays with the word after it (`HTTPServer` is one piece), which only ever
// makes a token look more like an identifier.
func boundary(r []rune, i int) bool {
	prev, cur := r[i-1], r[i]
	switch {
	case unicode.IsDigit(prev) != unicode.IsDigit(cur):
		return true
	case unicode.IsLower(prev) && unicode.IsUpper(cur):
		return true
	}
	return false
}
