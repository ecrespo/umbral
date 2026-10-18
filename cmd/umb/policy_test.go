package main

import (
	"bytes"
	"testing"
)

var explainAnswer = map[string]any{
	"decision": "ask", "deciding_rule": "destructive_pattern", "reason": "destructive", "allow_always": false,
	"trace": []map[string]any{
		{"step": "exposure", "matched": false, "decided": false},
		{"step": "destructive_pattern", "matched": true, "decided": true, "detail": "rm_recursive"},
		{"step": "deny_rules", "matched": false, "decided": false},
		{"step": "taint", "matched": false, "decided": false},
		{"step": "mode", "matched": false, "decided": false, "detail": "normal"},
		{"step": "allow_rules", "matched": true, "decided": false, "detail": "run_command rm *", "note": "overridden by destructive_pattern"},
		{"step": "mode_default", "matched": true, "decided": false, "detail": "normal"},
	},
}

// TestUmbPolicyExplain_REQ_SEC_009: `umb policy explain` sends the candidate action as API
// §5.36's params and prints the decision, the rule that decided and every step in order.
func TestUmbPolicyExplain_REQ_SEC_009(t *testing.T) {
	t.Parallel()
	seen := make(chan map[string]any, 4)
	socket := serveFakeDaemonWithParams(t, map[string]any{"policy.explain": explainAnswer}, seen)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"policy", "explain", "--mode", "normal", "--tool", "run_command", "--risk", "Exec", "--tainted",
		"--socket", socket, "--no-autostart", "--", "rm", "-rf", "build",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	var got map[string]any
	for p := range seen {
		if _, ok := p["tool"]; ok {
			got = p
			break
		}
	}
	want := map[string]any{"mode": "normal", "tool": "run_command", "risk": "Exec", "target": "rm -rf build", "tainted": true}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("params[%s] = %v, want %v", k, got[k], v)
		}
	}
	wantOut := "ask, decided by destructive_pattern (destructive); never remembered as always\n" +
		"  exposure             -\n" +
		"  destructive_pattern  decided  rm_recursive\n" +
		"  deny_rules           -\n" +
		"  taint                -\n" +
		"  mode                 -        normal\n" +
		"  allow_rules          matched  run_command rm * (overridden by destructive_pattern)\n" +
		"  mode_default         matched  normal\n"
	if stdout.String() != wantOut {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout.String(), wantOut)
	}
}

// TestUmbPolicyExplainNeedsAToolAndARisk: §5.36 requires both, and a mode or a thread; `umb`
// says so before it connects.
func TestUmbPolicyExplainNeedsAToolAndARisk(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"policy", "explain", "--risk", "Exec", "--mode", "normal", "ls"},
		{"policy", "explain", "--tool", "run_command", "--mode", "normal", "ls"},
		{"policy", "explain", "--tool", "run_command", "--risk", "Exec", "ls"},
		{"policy", "nope"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append(args, "--socket", "/nonexistent/umbral.sock", "--no-autostart"), &stdout, &stderr); code != exitFailure {
			t.Errorf("%v: exit = %d, want %d", args, code, exitFailure)
		}
		if stderr.Len() == 0 {
			t.Errorf("%v: nothing on stderr", args)
		}
	}
}
