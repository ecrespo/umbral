package ghostty

import (
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// TestSendKeysUsesTheEmulatorsEncoder_REQ_FTU_001: a named key is encoded by libghostty's key
// encoder set from the terminal's own modes, so the same `up` reaches a shell as `ESC [ A` and
// a program that asked for application cursor keys as `ESC O A` (delta
// `2026-10-terminal-interact`, decision 2).
func TestSendKeysUsesTheEmulatorsEncoder_REQ_FTU_001(t *testing.T) {
	emu, err := NewEmulator(domain.Size{Cols: 80, Rows: 24}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer emu.Close()
	enc := func(k domain.Key) string {
		t.Helper()
		b, err := emu.EncodeKey(k)
		if err != nil {
			t.Fatalf("%+v: %v", k, err)
		}
		return string(b)
	}

	for k, want := range map[domain.Key]string{
		{Name: "up"}:               "\x1b[A",
		{Name: "enter"}:            "\r",
		{Name: "tab"}:              "\t",
		{Name: "backspace"}:        "\x7f",
		{Name: "escape"}:           "\x1b",
		{Name: "space"}:            " ",
		{Name: "c", Ctrl: true}:    "\x03",
		{Name: "d", Ctrl: true}:    "\x04",
		{Name: "x", Alt: true}:     "\x1bx",
		{Name: "delete"}:           "\x1b[3~",
		{Name: "f1"}:               "\x1bOP",
		{Name: "page_up"}:          "\x1b[5~",
		{Name: "left", Ctrl: true}: "\x1b[1;5D",
	} {
		if got := enc(k); got != want {
			t.Errorf("%+v = %q, want %q", k, got, want)
		}
	}

	// DECCKM: the program asks for application cursor keys.
	if _, err := emu.Write([]byte("\x1b[?1h")); err != nil {
		t.Fatal(err)
	}
	if got := enc(domain.Key{Name: "up"}); got != "\x1bOA" {
		t.Errorf("up in application cursor mode = %q, want ESC O A", got)
	}
	if _, err := emu.Write([]byte("\x1b[?1l")); err != nil {
		t.Fatal(err)
	}
	if got := enc(domain.Key{Name: "up"}); got != "\x1b[A" {
		t.Errorf("up after the mode is reset = %q", got)
	}

	if _, err := emu.EncodeKey(domain.Key{Name: "hyper"}); err == nil {
		t.Error("an unknown key encoded")
	}
}
