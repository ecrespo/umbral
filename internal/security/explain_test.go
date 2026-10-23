package security

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/security/ports"
)

// fakeSource is the threads and rules policy.explain reads. It has no way to write, which is
// the read-only half of API §5.36 by construction.
type fakeSource struct {
	threads map[string]ports.ThreadPolicy
	rules   []domain.Rule
}

func (f fakeSource) ThreadPolicy(_ context.Context, id string) (ports.ThreadPolicy, error) {
	t, ok := f.threads[id]
	if !ok {
		return ports.ThreadPolicy{}, domain.ErrUnknownThread
	}
	return t, nil
}

func (f fakeSource) Rules(_ context.Context, threadID string) ([]domain.Rule, error) {
	var out []domain.Rule
	for _, r := range f.rules {
		if r.ThreadID == "" || r.ThreadID == threadID {
			out = append(out, r)
		}
	}
	return out, nil
}

// fakeCatalog declares three tools and resolves a path by joining it to the directory and
// marking it, so a test sees that the rules were given the tool's path and not the caller's.
type fakeCatalog struct{}

func (fakeCatalog) Risk(tool string) (domain.Risk, bool) {
	r, ok := map[string]domain.Risk{
		"run_command": domain.RiskExec, "write_file": domain.RiskWriteFS, "read_file": domain.RiskReadOnly,
	}[tool]
	return r, ok
}

func (fakeCatalog) PolicyPath(cwd, p string) string {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func newExplainer(t *testing.T) *Explainer {
	t.Helper()
	e, err := NewExplainer(ExplainConfig{
		Tools: fakeCatalog{},
		Source: fakeSource{
			threads: map[string]ports.ThreadPolicy{
				"thr_edit":   {Mode: domain.ModeAutoEdit, Cwd: "/work/repo/sub"},
				"thr_normal": {Mode: domain.ModeNormal, Cwd: "/work/repo"},
				"thr_wt":     {Mode: domain.ModeAutoEdit, Cwd: "/data/wt/thr_wt/sub", WriteRoot: "/data/wt/thr_wt"},
				"thr_full":   {Mode: domain.ModeFullAuto, Cwd: "/data/wt/thr_full/sub", WriteRoot: "/data/wt/thr_full"},
			},
			rules: []domain.Rule{
				{Tool: "run_command", Pattern: "curl *", Decision: domain.VerdictDeny, Source: domain.RuleSourceConfig},
				{Tool: "run_command", Pattern: "go test *", Decision: domain.VerdictAllow, Source: domain.RuleSourceUser},
				{ThreadID: "thr_normal", Tool: "fetch_url", Decision: domain.VerdictAllow, Source: domain.RuleSourceUser},
				{Tool: "read_file", Pattern: "/work/repo/secrets/*", Decision: domain.VerdictDeny, Source: domain.RuleSourceConfig},
			},
		},
		IsRepo: func(dir string) bool { return dir == "/work/repo" },
	})
	if err != nil {
		t.Fatalf("NewExplainer: %v", err)
	}
	return e
}

// TestExplainNamesDecidingRule_REQ_SEC_009: policy.explain answers the decision and the
// ordered trace — the exposure step and DD-006's six, every one evaluated — naming the one
// that decided. One case per step.
func TestExplainNamesDecidingRule_REQ_SEC_009(t *testing.T) {
	t.Parallel()
	e := newExplainer(t)
	for _, tc := range []struct {
		name    string
		req     domain.ExplainRequest
		verdict domain.Verdict
		step    domain.Step
		reason  string
	}{
		{
			"exposure",
			domain.ExplainRequest{Mode: domain.ModeAsk, Tool: "run_command", Risk: domain.RiskExec, Target: "ls"},
			domain.VerdictDeny, domain.StepExposure, domain.ReasonNotExposed,
		},
		{
			"destructive pattern",
			domain.ExplainRequest{Mode: domain.ModeNormal, Tool: "run_command", Risk: domain.RiskExec, Target: "rm -rf build"},
			domain.VerdictAsk, domain.StepDestructive, domain.ReasonDestructive,
		},
		{
			"deny rule",
			domain.ExplainRequest{Mode: domain.ModeNormal, Tool: "run_command", Risk: domain.RiskExec, Target: "curl example.com"},
			domain.VerdictDeny, domain.StepDenyRules, domain.ReasonDenyRule,
		},
		{
			"taint",
			domain.ExplainRequest{Mode: domain.ModeNormal, Tool: "run_command", Risk: domain.RiskExec, Target: "go test ./...", Tainted: true},
			domain.VerdictAsk, domain.StepTaint, domain.ReasonTainted,
		},
		{
			"mode",
			domain.ExplainRequest{ThreadID: "thr_edit", Tool: "write_file", Risk: domain.RiskWriteFS, Target: "main.go"},
			domain.VerdictAllow, domain.StepMode, "",
		},
		// The write root is the repository above the thread's directory, not the directory.
		{
			"mode in the repository above the cwd",
			domain.ExplainRequest{ThreadID: "thr_edit", Tool: "write_file", Risk: domain.RiskWriteFS, Target: "../go.mod"},
			domain.VerdictAllow, domain.StepMode, "",
		},
		{
			"mode outside the write root",
			domain.ExplainRequest{ThreadID: "thr_edit", Tool: "write_file", Risk: domain.RiskWriteFS, Target: "/etc/hosts"},
			domain.VerdictAsk, domain.StepMode, domain.ReasonOutsideWriteRoot,
		},
		{
			"allow rule",
			domain.ExplainRequest{Mode: domain.ModeNormal, Tool: "run_command", Risk: domain.RiskExec, Target: "go test ./..."},
			domain.VerdictAllow, domain.StepAllowRules, "",
		},
		{
			"a thread's own allow rule",
			domain.ExplainRequest{ThreadID: "thr_normal", Tool: "fetch_url", Risk: domain.RiskNetwork, Target: "https://go.dev"},
			domain.VerdictAllow, domain.StepAllowRules, "",
		},
		{
			"mode default allows ReadOnly",
			domain.ExplainRequest{Mode: domain.ModeNormal, Tool: "read_file", Risk: domain.RiskReadOnly, Target: "a.go"},
			domain.VerdictAllow, domain.StepModeDefault, "",
		},
		{
			"mode default asks",
			domain.ExplainRequest{Mode: domain.ModeNormal, Tool: "run_command", Risk: domain.RiskExec, Target: "make"},
			domain.VerdictAsk, domain.StepModeDefault, domain.ReasonPolicy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := e.Explain(t.Context(), tc.req)
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			if d.Verdict != tc.verdict || d.Step != tc.step || d.Reason != tc.reason {
				t.Errorf("decision = %s by %s (%q), want %s by %s (%q)", d.Verdict, d.Step, d.Reason, tc.verdict, tc.step, tc.reason)
			}
			order := []domain.Step{
				domain.StepExposure, domain.StepDestructive, domain.StepDenyRules,
				domain.StepTaint, domain.StepMode, domain.StepAllowRules, domain.StepModeDefault,
			}
			if len(d.Trace) != len(order) {
				t.Fatalf("trace has %d steps, want %d", len(d.Trace), len(order))
			}
			decided := 0
			for i, s := range d.Trace {
				if s.Step != order[i] {
					t.Errorf("trace[%d] = %s, want %s", i, s.Step, order[i])
				}
				if s.Decided {
					decided++
					if s.Step != tc.step {
						t.Errorf("trace marks %s decided, want %s", s.Step, tc.step)
					}
				}
			}
			if decided != 1 {
				t.Errorf("%d steps marked decided, want exactly 1", decided)
			}
		})
	}
}

// TestExplainUsesTheThreadUnlessAModeIsGiven: a thread lends its mode, write root and rules;
// a mode in the request asks "what if" and wins over the thread's.
func TestExplainUsesTheThreadUnlessAModeIsGiven(t *testing.T) {
	t.Parallel()
	e := newExplainer(t)
	req := domain.ExplainRequest{ThreadID: "thr_edit", Tool: "write_file", Risk: domain.RiskWriteFS, Target: "main.go"}
	if d, _ := e.Explain(t.Context(), req); d.Verdict != domain.VerdictAllow {
		t.Errorf("the thread's auto-edit: %s, want allow", d.Verdict)
	}
	req.Mode = domain.ModeAsk
	if d, _ := e.Explain(t.Context(), req); d.Verdict != domain.VerdictDeny || d.Step != domain.StepExposure {
		t.Errorf("mode ask over the thread: %s by %s, want deny by exposure", d.Verdict, d.Step)
	}
	// Without a thread there is no write root: auto-edit cannot allow a write.
	req = domain.ExplainRequest{Mode: domain.ModeAutoEdit, Tool: "write_file", Risk: domain.RiskWriteFS, Target: "main.go"}
	if d, _ := e.Explain(t.Context(), req); d.Reason != domain.ReasonOutsideWriteRoot {
		t.Errorf("auto-edit without a thread: %+v, want outside_write_root", d)
	}
}

// TestExplainShowsFullAuto_REQ_ORC_004: a thread in full-auto inside its worktree explains a
// command there as allowed by the mode step, a fetch as asked; `mode: "full-auto"` asks the same
// "what if" of any thread.
func TestExplainShowsFullAuto_REQ_ORC_004(t *testing.T) {
	t.Parallel()
	e := newExplainer(t)
	req := domain.ExplainRequest{ThreadID: "thr_full", Tool: "run_command", Risk: domain.RiskExec, Target: "make"}
	if d, err := e.Explain(t.Context(), req); err != nil || d.Verdict != domain.VerdictAllow || d.Step != domain.StepMode {
		t.Errorf("a command in full-auto: %+v, %v; want allow by the mode step", d, err)
	}
	req = domain.ExplainRequest{ThreadID: "thr_full", Tool: "fetch_url", Risk: domain.RiskNetwork, Target: "https://example.com"}
	if d, _ := e.Explain(t.Context(), req); d.Verdict != domain.VerdictAsk {
		t.Errorf("a fetch in full-auto: %s, want ask", d.Verdict)
	}
	req = domain.ExplainRequest{ThreadID: "thr_wt", Mode: domain.ModeFullAuto, Tool: "run_command", Risk: domain.RiskExec, Target: "make"}
	if d, err := e.Explain(t.Context(), req); err != nil || d.Verdict != domain.VerdictAllow {
		t.Errorf("mode full-auto over a worktree thread: %+v, %v; want allow", d, err)
	}
}

// TestExplainRefusesWhatItCannotEvaluate: an unknown thread is NOT_FOUND; a request with no
// mode and no thread, an unknown mode or risk, or no tool is a validation error.
func TestExplainRefusesWhatItCannotEvaluate(t *testing.T) {
	t.Parallel()
	e := newExplainer(t)
	if _, err := e.Explain(t.Context(), domain.ExplainRequest{ThreadID: "thr_nope", Tool: "read_file", Risk: domain.RiskReadOnly}); !errors.Is(err, domain.ErrUnknownThread) {
		t.Errorf("unknown thread: %v", err)
	}
	for name, req := range map[string]domain.ExplainRequest{
		"no mode, no thread": {Tool: "read_file", Risk: domain.RiskReadOnly},
		"unknown mode":       {Mode: "yolo", Tool: "read_file", Risk: domain.RiskReadOnly},
		"unknown risk":       {Mode: domain.ModeNormal, Tool: "read_file", Risk: "Root"},
		"no tool":            {Mode: domain.ModeNormal, Risk: domain.RiskReadOnly},
	} {
		if _, err := e.Explain(t.Context(), req); !errors.Is(err, domain.ErrInvalidExplain) {
			t.Errorf("%s: %v, want ErrInvalidExplain", name, err)
		}
	}
}

// TestExplainShapesTheActionAsTheToolDoes: a turn decides on the risk the tool declared and on
// a path made absolute against the thread's directory, so an explanation does too. A risk that
// contradicts a registered tool is refused; an unregistered tool — a server not connected yet —
// is evaluated as given.
func TestExplainShapesTheActionAsTheToolDoes(t *testing.T) {
	t.Parallel()
	e := newExplainer(t)
	// The rule names an absolute path; the request a relative one under the thread's directory.
	d, err := e.Explain(t.Context(), domain.ExplainRequest{
		ThreadID: "thr_normal", Tool: "read_file", Risk: domain.RiskReadOnly, Target: "secrets/prod.env",
	})
	if err != nil || d.Step != domain.StepDenyRules {
		t.Errorf("a relative path under an absolute deny rule: %s by %s (%v), want deny by deny_rules", d.Verdict, d.Step, err)
	}
	if _, err := e.Explain(t.Context(), domain.ExplainRequest{
		Mode: domain.ModeNormal, Tool: "run_command", Risk: domain.RiskReadOnly, Target: "rm -rf /",
	}); !errors.Is(err, domain.ErrInvalidExplain) {
		t.Errorf("run_command as ReadOnly: %v, want ErrInvalidExplain", err)
	}
	if d, err := e.Explain(t.Context(), domain.ExplainRequest{
		Mode: domain.ModeNormal, Tool: "mcp_gitlab_merge", Risk: domain.RiskNetwork, Target: "!42",
	}); err != nil || d.Verdict != domain.VerdictAsk {
		t.Errorf("an unregistered tool: %+v %v, want ask", d, err)
	}
}

// TestExplainUsesTheThreadsWorktree_REQ_WT_002: a thread running in its worktree lends the
// worktree's directory and write root, as its turn decides with them: a relative write lands
// in the worktree and is allowed in auto-edit, and a write to the main copy is outside the
// write root and asks.
func TestExplainUsesTheThreadsWorktree_REQ_WT_002(t *testing.T) {
	t.Parallel()
	e := newExplainer(t)
	for target, want := range map[string]domain.Verdict{
		"main.go":            domain.VerdictAllow,
		"../go.mod":          domain.VerdictAllow,
		"/work/repo/main.go": domain.VerdictAsk,
	} {
		d, err := e.Explain(t.Context(), domain.ExplainRequest{ThreadID: "thr_wt", Tool: "write_file", Risk: domain.RiskWriteFS, Target: target})
		if err != nil {
			t.Fatal(err)
		}
		if d.Verdict != want {
			t.Errorf("write %s = %s, want %s", target, d.Verdict, want)
		}
	}
}
