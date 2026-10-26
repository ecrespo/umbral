package domain

import (
	"errors"
	"strings"
	"testing"
)

func text(s string) Key  { return Key{Text: s} }
func named(n string) Key { return Key{Name: n} }
func ctrl(n string) Key  { return Key{Name: n, Ctrl: true} }
func alt(n string) Key   { return Key{Name: n, Alt: true} }
func atPrompt(line string) InteractState {
	return InteractState{AtPrompt: true, Line: line}
}

// TestKeysAreValidated_REQ_FTU_001: a key is a printable text or a named key, never raw bytes
// or an escape sequence that could type what no approval saw (delta
// `2026-10-terminal-interact`, decision 2).
func TestKeysAreValidated_REQ_FTU_001(t *testing.T) {
	good := [][]Key{
		{text("ls -la"), named("enter")},
		{named("up"), named("page_down"), named("f12"), ctrl("c"), alt("x"), ctrl("7")},
		{text("héllo ✓")},
	}
	for _, keys := range good {
		if err := ValidateKeys(keys); err != nil {
			t.Errorf("%+v: %v", keys, err)
		}
	}
	bad := [][]Key{
		nil,
		{text("")},
		{text("a\nb")},
		{text("a\rb")},
		{text("\x1b[A")},
		{text("tab\there")},
		{text("del\x7f")},
		{text("\x9b")},
		{text(string([]byte{0xff}))},
		{named("q")},
		{named("hyper")},
		{Key{Text: "a", Name: "enter"}},
		{Key{Text: "a", Ctrl: true}},
		{ctrl("enterx")},
		{text(strings.Repeat("x", MaxKeysText+1))},
		make([]Key, MaxKeys+1),
	}
	for _, keys := range bad {
		if err := ValidateKeys(keys); !errors.Is(err, ErrValidation) {
			t.Errorf("%+v: err = %v, want ErrValidation", keys, err)
		}
	}
}

// TestTheLineIsAssembledAcrossCalls_REQ_FTU_003: at the shell's prompt only text, backspace
// and a final enter or ctrl+c are accepted, and the line is kept across calls until an enter
// submits it (decision 4).
func TestTheLineIsAssembledAcrossCalls_REQ_FTU_003(t *testing.T) {
	plan, err := PlanKeys(atPrompt(""), []Key{text("rm -")})
	if err != nil || plan.Submit || plan.Line != "rm -" {
		t.Fatalf("first call: %+v, %v", plan, err)
	}
	plan, err = PlanKeys(atPrompt(plan.Line), []Key{text("rf /x"), named("backspace"), named("enter")})
	if err != nil || !plan.Submit || plan.Line != "rm -rf /" {
		t.Fatalf("second call: %+v, %v", plan, err)
	}
	plan, err = PlanKeys(atPrompt("echo half"), []Key{text("x"), ctrl("c")})
	if err != nil || plan.Submit || !plan.Interrupt || plan.Line != "" {
		t.Errorf("ctrl+c empties the line: %+v, %v", plan, err)
	}
	plan, err = PlanKeys(atPrompt("é"), []Key{named("backspace"), named("backspace")})
	if err != nil || plan.Line != "" {
		t.Errorf("backspace takes back a rune: %+v, %v", plan, err)
	}

	for _, keys := range [][]Key{
		{text("ls"), named("tab")},
		{named("up")},
		{ctrl("r")},
		{alt("b")},
		{named("enter"), text("ls")},
		{ctrl("c"), text("ls")},
		{named("escape")},
	} {
		if _, err := PlanKeys(atPrompt(""), keys); !errors.Is(err, ErrValidation) {
			t.Errorf("%+v at the prompt: err = %v, want ErrValidation", keys, err)
		}
	}

	// A program's input is not a shell line: any key goes, nothing is assembled.
	plan, err = PlanKeys(InteractState{}, []Key{text("SELECT 1;"), named("enter"), named("up"), named("tab"), named("enter")})
	if err != nil || plan.Submit || plan.Line != "" {
		t.Errorf("program input: %+v, %v", plan, err)
	}
}
