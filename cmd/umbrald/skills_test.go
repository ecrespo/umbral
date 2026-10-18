package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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
