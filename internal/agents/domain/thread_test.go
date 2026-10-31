package domain

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestACwdIsAbsoluteByThePlatformsRules_REQ_WIN_001: a thread's cwd is absolute by the
// platform's own reading, so a Windows path with its drive is taken and a rooted path without
// one is refused there, as a relative one is everywhere (delta `2026-10-windows-port`).
func TestACwdIsAbsoluteByThePlatformsRules_REQ_WIN_001(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs("work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (CreateParams{Cwd: abs}).Validate(); err != nil {
		t.Errorf("Validate(%q) = %v, want it taken", abs, err)
	}
	for _, cwd := range []string{"", "work", filepath.Join("..", "work")} {
		if _, err := (CreateParams{Cwd: cwd}).Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("Validate(%q) = %v, want ErrValidation", cwd, err)
		}
	}
	if _, err := (CreateParams{Cwd: `\work`}).Validate(); filepath.IsAbs(`\work`) == (err != nil) {
		t.Errorf(`Validate("\work") = %v, want the platform's filepath.IsAbs`, err)
	}
}
