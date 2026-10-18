package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/store"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
)

// TestTheDaemonLoadsLocalRules_REQ_SEC_010: a file in `rules/` beside config.toml reaches
// policy.explain and redaction through the one rule set the daemon builds at start.
func TestTheDaemonLoadsLocalRules_REQ_SEC_010(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	settingsPath := filepath.Join(t.TempDir(), "umbral", "config.toml")
	dir := rulesDir(settingsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), []byte(`{"redaction": [{"name": "acme_token", "pattern": "acme_[0-9]{8}"}],
		"destructive": [{"name": "nuke", "pattern": "^nuke( |$)"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	active := loadRules(slog.New(slog.NewTextHandler(io.Discard, nil)), settingsPath)

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tools, err := registry.New(builtin.All(builtin.Config{})...)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newExplainer(db, tools, active.Policy)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Explain(ctx, secdomain.ExplainRequest{Mode: secdomain.ModeAutoEdit, Tool: "run_command", Risk: secdomain.RiskExec, Target: "nuke build"})
	if err != nil || d.Step != secdomain.StepDestructive || d.Reason != secdomain.ReasonDestructive {
		t.Errorf("explain = %s by %s (%v), want the local destructive pattern to decide", d.Verdict, d.Step, err)
	}
	if got := active.Redact("key acme_12345678 and AKIAIOSFODNN7EXAMPLE").Text; strings.Contains(got, "acme_12345678") || strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("redaction = %q, want the local and the built-in rules applied", got)
	}
}
