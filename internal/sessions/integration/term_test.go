package integration_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// TestAPaneAdvertisesTheTerminalThatRendersIt_REQ_TERM_013: every pane says it is the
// emulator that renders it — `TERM=xterm-256color`, `COLORTERM=truecolor` — whatever terminal
// the daemon itself was started from.
//
// The daemon used to hand its own environment down unchanged. Started by systemd, launchd or
// a CI runner it has no TERM, and bash then runs readline as a dumb terminal: horizontal
// scrolling, and the first byte typed at a fresh prompt lost — which is how REQ-TERM-011's
// pending command and REQ-TERM-007's `tput cols` failed on every GitHub runner. Started inside
// tmux it has `TERM=screen`, and every pane would lie about what it is.
//
// Not parallel: it sets the process environment the daemon inherits from.
func TestAPaneAdvertisesTheTerminalThatRendersIt_REQ_TERM_013(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh is not installed: %v", err)
	}
	t.Setenv("TERM", "screen")
	t.Setenv("COLORTERM", "")
	if err := os.Unsetenv("COLORTERM"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)

	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"the daemon's own terminal is not the pane's", nil, "term=xterm-256color color=truecolor"},
		{"a pane's declared environment still wins", map[string]string{"TERM": "vt100"}, "term=vt100 color=truecolor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := h.create(t, domain.CreateParams{
				Shell: "/bin/sh", Env: tc.env,
				Command: []string{"sh", "-c", `printf 'term=%s color=%s\n' "$TERM" "$COLORTERM"; sleep 5`},
			})
			if !eventuallyContains(t, h, session.ID, tc.want) {
				snapshot, _ := h.Snapshot(t.Context(), session.ID)
				t.Errorf("the pane never printed %q; screen was:\n%s", tc.want, snapshot.Data)
			}
		})
	}
}
