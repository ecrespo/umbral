package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui/ports"
)

// TestAPluginKeyTypesItsCommandWithoutRunningIt_REQ_PLG_002: a plugin's key types its command,
// quoted, at the focused pane's prompt, and no Enter: the user runs it, so it is the user's
// command (delta `2026-10-plugins`, decision 8). A key no plugin binds still reaches the shell,
// a binding installed later is picked up on plugin.changed, and a pane where a program rather
// than a prompt holds the cursor is typed nothing, since its keys would be commands to it.
func TestAPluginKeyTypesItsCommandWithoutRunningIt_REQ_PLG_002(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.openSession()
	ctrlAltD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl | tea.ModAlt}

	h.step(ctrlAltD)
	if got := h.daemon.inputs(); len(got) != 1 || string(got[0]) == "make deploy" {
		t.Fatalf("an unbound ctrl+alt+d = %q, want it passed to the shell", got)
	}

	h.daemon.mu.Lock()
	h.daemon.pluginKeys = []ports.PluginKey{{Key: "ctrl+alt+d", Plugin: "acme", Command: "deploy", Argv: []string{"make", "deploy", "it's live"}}}
	h.daemon.mu.Unlock()
	h.event(ports.Event{Kind: ports.EventPluginChanged})

	h.step(ctrlAltD)
	got := h.daemon.inputs()
	if want := `make deploy 'it'\''s live'`; len(got) != 2 || string(got[1]) != want {
		t.Fatalf("the bound key typed %q, want %q", got, want)
	}

	h.screen.busy = true
	h.step(ctrlAltD)
	if got := h.daemon.inputs(); len(got) != 2 {
		t.Fatalf("a pane running a program was typed %q", got[2:])
	}
	if !strings.Contains(h.m.status, "not at a prompt") {
		t.Errorf("status = %q, want it to say why nothing was typed", h.m.status)
	}
}
