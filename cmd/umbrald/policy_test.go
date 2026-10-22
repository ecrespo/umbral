package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/adapters/threadstore"
	agentsdomain "github.com/ecrespo/umbral/internal/agents/domain"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
)

// TestExplainReadsWhatATurnDecidesWith_REQ_SEC_009: against the real store, policy.explain
// takes a thread's mode, the repository root above its directory, its own rules and the
// global ones, as the agent runtime does.
func TestExplainReadsWhatATurnDecidesWith_REQ_SEC_009(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The thread's directory is reached through a link, as a turn's tools see it resolved.
	real := t.TempDir()
	if err := os.Mkdir(filepath.Join(real, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	real, err = filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(link, "pkg")
	threads, err := threadstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	th := agentsdomain.Thread{
		ID: store.NewID(store.PrefixThread), Mode: secdomain.ModeAutoEdit, ModelClass: "code", Cwd: sub,
		State: agentsdomain.StateIdle, AttentionState: "idle", MaxSteps: 50, BudgetTokens: 1000, CreatedAt: 1, UpdatedAt: 1,
	}
	if err := threads.CreateThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO policy_rules (thread_id, tool, pattern, decision, source, created_at)
		VALUES (NULL, 'run_command', 'curl *', 'deny', 'config', 1), (?, 'run_command', 'make *', 'allow', 'user_decision', 1),
		       (NULL, 'read_file', ?, 'deny', 'config', 1)`, th.ID, filepath.Join(real, "pkg", "secrets", "*")); err != nil {
		t.Fatal(err)
	}

	tools, err := registry.New(builtin.All(builtin.Config{})...)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newExplainer(db, tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := func() (n int) {
		t.Helper()
		if err := db.DB().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM policy_rules) + (SELECT count(*) FROM approvals)
			+ (SELECT count(*) FROM tool_calls) + (SELECT count(*) FROM egress_log)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := rows()
	for _, tc := range []struct {
		name string
		req  secdomain.ExplainRequest
		step secdomain.Step
		v    secdomain.Verdict
	}{
		{"a write in the repository above the thread's directory", secdomain.ExplainRequest{ThreadID: th.ID, Tool: "write_file", Risk: secdomain.RiskWriteFS, Target: "../go.mod"}, secdomain.StepMode, secdomain.VerdictAllow},
		{"the global deny rule", secdomain.ExplainRequest{ThreadID: th.ID, Tool: "run_command", Risk: secdomain.RiskExec, Target: "curl x"}, secdomain.StepDenyRules, secdomain.VerdictDeny},
		{"the thread's allow rule", secdomain.ExplainRequest{ThreadID: th.ID, Tool: "run_command", Risk: secdomain.RiskExec, Target: "make all"}, secdomain.StepAllowRules, secdomain.VerdictAllow},
		{"a relative path, through the link, under a rule on the real path", secdomain.ExplainRequest{ThreadID: th.ID, Tool: "read_file", Risk: secdomain.RiskReadOnly, Target: "secrets/prod.env"}, secdomain.StepDenyRules, secdomain.VerdictDeny},
		{"without the thread its rule does not apply", secdomain.ExplainRequest{Mode: secdomain.ModeNormal, Tool: "run_command", Risk: secdomain.RiskExec, Target: "make all"}, secdomain.StepModeDefault, secdomain.VerdictAsk},
	} {
		d, err := e.Explain(ctx, tc.req)
		if err != nil || d.Step != tc.step || d.Verdict != tc.v {
			t.Errorf("%s: %s by %s (%v), want %s by %s", tc.name, d.Verdict, d.Step, err, tc.v, tc.step)
		}
	}
	if _, err := e.Explain(ctx, secdomain.ExplainRequest{ThreadID: store.NewID(store.PrefixThread), Tool: "read_file", Risk: secdomain.RiskReadOnly}); !errors.Is(err, secdomain.ErrUnknownThread) {
		t.Errorf("an unknown thread: %v, want ErrUnknownThread", err)
	}
	if _, err := e.Explain(ctx, secdomain.ExplainRequest{Mode: secdomain.ModeNormal, Tool: "run_command", Risk: secdomain.RiskReadOnly, Target: "rm -rf /"}); !errors.Is(err, secdomain.ErrInvalidExplain) {
		t.Errorf("run_command claimed ReadOnly: %v, want ErrInvalidExplain", err)
	}
	// Read-only: nothing was written by any of it.
	if after := rows(); after != before {
		t.Errorf("explaining wrote %d rows", after-before)
	}
}
