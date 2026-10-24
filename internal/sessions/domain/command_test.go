package domain

import "testing"

// TestAnAgentCommandWithAControlCharacterIsRefused_REQ_ACP_005: an agent's command is typed into
// an interactive shell, whose line editor acts on a control character — Ctrl-U erases the line so
// far — so a command holding one would not run as it was decided (run_command and an ACP terminal
// alike).
func TestAnAgentCommandWithAControlCharacterIsRefused_REQ_ACP_005(t *testing.T) {
	for _, c := range []string{"echo '\x15rm -rf ~ #'", "echo\x01", "a\tb", "a\x7f", "echo \x1b[A"} {
		if err := ValidateAgentCommand(c); err == nil {
			t.Errorf("%q accepted", c)
		}
	}
	if err := ValidateAgentCommand("printf 'héllo' && go test ./..."); err != nil {
		t.Errorf("a plain command was refused: %v", err)
	}
}
