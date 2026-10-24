package sessions

import (
	"errors"
	"os/exec"
	"testing"
)

// TestATerminalRunsOnlyInAPOSIXShell_REQ_ACP_005: an ACP terminal's line is quoted for a POSIX
// shell, so it runs in bash, else zsh, and never in fish, whose single quotes read `\'` as an
// escape; with neither, the terminal is refused.
func TestATerminalRunsOnlyInAPOSIXShell_REQ_ACP_005(t *testing.T) {
	only := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/bin/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	for _, tc := range []struct {
		have []string
		want string
	}{
		{[]string{"bash", "zsh", "fish"}, "/bin/bash"},
		{[]string{"zsh", "fish"}, "/bin/zsh"},
		{[]string{"fish"}, ""},
		{nil, ""},
	} {
		got, err := terminalShell(only(tc.have...))
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("with %v: %q, %v; want %q", tc.have, got, err, tc.want)
		}
		if err != nil && !errors.Is(err, errNoPOSIXShell) {
			t.Errorf("with %v: %v, want errNoPOSIXShell", tc.have, err)
		}
	}
}
