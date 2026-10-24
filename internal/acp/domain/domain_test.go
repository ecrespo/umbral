package domain

import "testing"

// TestAHostedToolCallIsNamedFromACPsKinds_REQ_ACP_002: a hosted call's `tool` is `acp:<kind>`
// over ACP's closed set, so a kind the agent made up — or one API §4 does not list, such as
// `switch_mode` — is `other`, and decided as a command.
func TestAHostedToolCallIsNamedFromACPsKinds_REQ_ACP_002(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"execute": "execute", "read": "read", "other": "other",
		"switch_mode": "other", "": "other", "rm -rf /": "other", "Execute": "other",
	} {
		if got := Kind(in); got != want {
			t.Errorf("Kind(%q) = %q, want %q", in, got, want)
		}
	}
	if Risk(Kind("switch_mode")) != "Exec" {
		t.Errorf("an unknown kind is %s, want Exec", Risk(Kind("switch_mode")))
	}
}
