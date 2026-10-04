package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/client"
)

// TestTheDaemonEnforcesTheCostCap_REQ_AGT_008: models.toml's `max_cost_usd_per_thread` reaches
// the agent runtime of a real daemon. A thread that has spent the cap answers thread.send with
// BUDGET_EXCEEDED, and no model call is made (delta `2026-10-cost-cap`).
func TestTheDaemonEnforcesTheCostCap_REQ_AGT_008(t *testing.T) {
	ollama := &scriptedOllama{}
	stream, ctx, _ := startAgentDaemon(t, ollama.server(t).URL, daemonOpts{router: "max_cost_usd_per_thread = 0.000001"})

	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	// The local model is free, so the spend is written as a priced provider's would be.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(os.Getenv("XDG_DATA_HOME"), "umbral", "umbral.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `UPDATE threads SET cost_micro_usd = 1 WHERE id = ?`, thread.ID); err != nil {
		t.Fatal(err)
	}

	err = stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "hello"}, nil)
	var rpc *client.Error
	if !errors.As(err, &rpc) || rpc.DomainCode != "BUDGET_EXCEEDED" {
		t.Fatalf("thread.send on a spent cap = %v, want BUDGET_EXCEEDED", err)
	}
	ollama.mu.Lock()
	calls := len(ollama.bodies)
	ollama.mu.Unlock()
	if calls != 0 {
		t.Fatalf("%d model calls on a spent cap", calls)
	}
}
