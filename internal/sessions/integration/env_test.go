package integration_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// TestEnvInjectedAndAuthoritative_REQ_INT_001: every process launched in a pane gets the seven
// UMBRAL_* variables of Tech Design §5.2b, and Umbral's values beat the ones the pane, or the
// layout it came from, declared. A hook that reads `UMBRAL_PANE_ID` reports for the pane it
// runs in, whatever a copied layout says.
func TestEnvInjectedAndAuthoritative_REQ_INT_001(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh is not installed: %v", err)
	}
	h := newHarness(t)

	names := []string{
		"UMBRAL_ENV", "UMBRAL_SOCKET_PATH", "UMBRAL_BIN_PATH", "UMBRAL_WORKSPACE_ID",
		"UMBRAL_TAB_ID", "UMBRAL_PANE_ID", "UMBRAL_SESSION_ID",
	}
	script := ""
	for _, name := range names {
		script += `printf '` + name + `=<%s>\n' "$` + name + `"; `
	}
	declared := map[string]string{}
	for _, name := range names {
		declared[name] = "forged"
	}
	session := h.create(t, domain.CreateParams{
		Shell: "/bin/sh", Env: declared,
		Pane:    domain.PaneLocation{WorkspaceID: "w3", TabID: "w3:t2", PaneID: "w3:p5"},
		Command: []string{"sh", "-c", script + "sleep 5"},
	})

	want := map[string]string{
		"UMBRAL_ENV":          "1",
		"UMBRAL_SOCKET_PATH":  testSocketPath,
		"UMBRAL_BIN_PATH":     testBinPath,
		"UMBRAL_WORKSPACE_ID": "w3",
		"UMBRAL_TAB_ID":       "w3:t2",
		"UMBRAL_PANE_ID":      "w3:p5",
		"UMBRAL_SESSION_ID":   session.ID,
	}
	for _, name := range names {
		line := name + "=<" + want[name] + ">"
		if !eventuallyContains(t, h, session.ID, line) {
			snapshot, _ := h.Snapshot(t.Context(), session.ID)
			t.Errorf("the pane never printed %q; screen was:\n%s", line,
				strings.TrimSpace(string(snapshot.Data)))
		}
	}
}

// TestNoPaneLocationIsInherited_REQ_INT_001: a session launched in no pane — a thread's PTY —
// carries no pane location at all, neither the one its caller declared nor the one the daemon
// inherited. A daemon autostarted by `umb` from another daemon's pane has that pane's
// UMBRAL_PANE_ID in its own environment.
//
// Not parallel: it sets the process environment the daemon inherits from.
func TestNoPaneLocationIsInherited_REQ_INT_001(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh is not installed: %v", err)
	}
	t.Setenv("UMBRAL_PANE_ID", "w9:p9")
	t.Setenv("UMBRAL_WORKSPACE_ID", "w9")
	h := newHarness(t)

	session := h.create(t, domain.CreateParams{
		Shell: "/bin/sh", Env: map[string]string{"UMBRAL_TAB_ID": "w8:t8"},
		Command: []string{
			"sh", "-c",
			`printf 'loc=<%s|%s|%s> env=<%s>\n' "${UMBRAL_WORKSPACE_ID-unset}" ` +
				`"${UMBRAL_TAB_ID-unset}" "${UMBRAL_PANE_ID-unset}" "$UMBRAL_ENV"; sleep 5`,
		},
	})
	const want = "loc=<unset|unset|unset> env=<1>"
	if !eventuallyContains(t, h, session.ID, want) {
		snapshot, _ := h.Snapshot(t.Context(), session.ID)
		t.Errorf("the pane never printed %q; screen was:\n%s", want,
			strings.TrimSpace(string(snapshot.Data)))
	}
}
