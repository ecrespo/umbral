package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/store"
)

// TestTheDaemonSweepsSkillsAtStart_REQ_SKL_002: the store lives beside the database, and
// building it settles what an interrupted install left, before any client is served.
func TestTheDaemonSweepsSkillsAtStart_REQ_SKL_002(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(dir, "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := bus.New()
	t.Cleanup(b.Close)
	if _, err := newSkills(t.Context(), db, b, slog.New(slog.DiscardHandler)); err != nil { // the first start claims it
		t.Fatalf("newSkills: %v", err)
	}
	leftover := filepath.Join(dir, "skills", ".staging-skl_x")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newSkills(t.Context(), db, b, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("newSkills after a crash: %v", err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging directory survived the start: %v", err)
	}
}

// TestTheDaemonListsSkillsAndLoadsThem_REQ_SKL_004 wires the store into a real daemon's turns: a
// skill installed over the socket is in the next prompt's catalog with skill_load among the
// tools, and the model's skill_load call returns its SKILL.md.
func TestTheDaemonListsSkillsAndLoadsThem_REQ_SKL_004(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"skill_load","arguments":{"name":"release-check"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":40,"eval_count":8}`},
		{`{"model":"m","message":{"role":"assistant","content":"Loaded."},"done":true,"done_reason":"stop","prompt_eval_count":60,"eval_count":5}`},
	}}
	srv := ollama.server(t)
	stream, ctx := agentDaemon(t, srv.URL)

	src := filepath.Join(t.TempDir(), "release-check")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "SKILL.md"), "---\nname: release-check\ndescription: Steps to cut a release.\n---\n# Release\n\nRUN THE CHECKLIST\n")
	var manifest struct {
		SHA256 string `json:"sha256"`
	}
	if err := stream.Call(ctx, "skill.inspect", map[string]any{"path": src}, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "skill.install", map[string]any{"path": src, "expected_sha256": manifest.SHA256}, nil); err != nil {
		t.Fatal(err)
	}

	var thread struct{ ID string }
	if err := stream.Call(ctx, "thread.create", map[string]any{"cwd": t.TempDir()}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := stream.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "cut a release"}, nil); err != nil {
		t.Fatal(err)
	}
	var statuses []string
wait:
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "thread.tool_call":
				var tc struct{ Tool, Status string }
				_ = n.Decode(&tc)
				statuses = append(statuses, tc.Tool+":"+tc.Status)
			case "thread.turn_finished":
				break wait
			}
		case <-ctx.Done():
			t.Fatal("the turn did not finish")
		}
	}
	if strings.Join(statuses, ",") != "skill_load:pending,skill_load:ok" {
		t.Fatalf("tool call statuses %v", statuses)
	}
	ollama.mu.Lock()
	bodies := append([]string(nil), ollama.bodies...)
	ollama.mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("%d model calls, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], "release-check: Steps to cut a release.") || !strings.Contains(bodies[0], `"skill_load"`) {
		t.Errorf("the first prompt lacks the catalog or skill_load: %s", bodies[0])
	}
	if strings.Contains(bodies[0], "RUN THE CHECKLIST") {
		t.Error("the body reached the prompt before skill_load")
	}
	if !strings.Contains(bodies[1], "RUN THE CHECKLIST") {
		t.Errorf("the model never read the skill's body: %s", bodies[1])
	}
}
