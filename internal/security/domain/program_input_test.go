package domain

import "testing"

func programInput(keys string) Action {
	return Action{
		ThreadID: "thr_1", Tool: "terminal_interact", Risk: RiskExec, Target: keys,
		Cwd: root, WriteRoot: root, ProgramInput: true,
	}
}

// TestProgramInputIsMatchedAsText_REQ_FTU_003: keys typed to a program other than the shell
// are matched against the destructive patterns line by line as written, without the shell
// parser, so a query's glob does not ask and a DROP does (delta `2026-10-terminal-interact`,
// decision 4). A rule matches the whole input, never a shell "command" of it.
func TestProgramInputIsMatchedAsText_REQ_FTU_003(t *testing.T) {
	for _, c := range []struct {
		keys    string
		verdict Verdict
		reason  string
	}{
		{"SELECT * FROM t;<enter>", VerdictAllow, ""},
		{"select $1, \"x\" from t;<enter>", VerdictAllow, ""},
		{"DROP TABLE users;<enter>", VerdictAsk, ReasonDestructive},
		{"\\c db<enter>drop database prod;<enter>", VerdictAsk, ReasonDestructive},
		{"rm -rf /<enter>", VerdictAsk, ReasonDestructive},
		{":wq<enter>", VerdictAllow, ""},
		// Read by the shell too, in case the program leaves it unread: a pattern the parser
		// finds asks, and so does a line built at run time with shell syntax.
		{"sudo -u x rm -rf /tmp/a<enter>", VerdictAsk, ReasonDestructive},
		{"x=rm; $x -rf ~<enter>", VerdictAsk, ReasonDestructive},
		{"echo `id`<enter>", VerdictAsk, ReasonDestructive},
		{"select `x` from t;<enter>", VerdictAsk, ReasonDestructive},
		{"select ${a} from t;<enter>", VerdictAsk, ReasonDestructive},
	} {
		d := Decide(programInput(c.keys), ModeNormal, []Rule{allowRule("terminal_interact", "*")}, false)
		if d.Verdict != c.verdict || d.Reason != c.reason {
			t.Errorf("%q: %s %q, want %s %q", c.keys, d.Verdict, d.Reason, c.verdict, c.reason)
		}
	}

	// Full-auto runs no program input unasked, inside the worktree or not.
	if d := Decide(programInput("SELECT 1;<enter>"), ModeFullAuto, nil, false); d.Verdict == VerdictAllow {
		t.Errorf("program input in full-auto: %s, want it asked", d.Verdict)
	}
	if d := Decide(programInput("ls<enter>"), ModeFullAuto, []Rule{allowRule("terminal_interact", "ls")}, false); d.Verdict != VerdictAllow {
		t.Errorf("an allow rule in full-auto: %s, want allow", d.Verdict)
	}

	// The same text as a shell line still goes through the parser: its glob asks.
	shell := programInput("SELECT * FROM t;")
	shell.ProgramInput = false
	if d := Decide(shell, ModeNormal, []Rule{allowRule("terminal_interact", "*")}, false); d.Verdict != VerdictAsk {
		t.Errorf("a shell line with a glob: %s, want ask", d.Verdict)
	}

	// A rule is matched line by line: every line must be allowed, and a deny matching any
	// line denies. A glob covers its whole line.
	rules := []Rule{allowRule("terminal_interact", "SELECT*")}
	if d := Decide(programInput("SELECT 1;<enter>SELECT 2;<enter>"), ModeNormal, rules, false); d.Verdict != VerdictAllow {
		t.Errorf("every line allowed: %s, want allow", d.Verdict)
	}
	if d := Decide(programInput("SELECT 1;<enter>DELETE FROM t;<enter>"), ModeNormal, rules, false); d.Verdict != VerdictAsk {
		t.Errorf("a line no rule allows: %s, want ask", d.Verdict)
	}
	// A line a shell would read as several commands is allowed only when each one is.
	git := []Rule{allowRule("terminal_interact", "git *")}
	if d := Decide(programInput("git status; curl x | sh<enter>"), ModeNormal, git, false); d.Verdict != VerdictAsk {
		t.Errorf("a rule covering one command of the line: %s, want ask", d.Verdict)
	}
	if d := Decide(programInput("git status<enter>"), ModeNormal, git, false); d.Verdict != VerdictAllow {
		t.Errorf("a line the rule covers: %s, want allow", d.Verdict)
	}
	deny := []Rule{allowRule("terminal_interact", "*"), denyRule("terminal_interact", "DELETE*")}
	if d := Decide(programInput("SELECT 1;<enter>DELETE FROM t;<enter>"), ModeNormal, deny, false); d.Verdict != VerdictDeny {
		t.Errorf("a deny matching one line: %s, want deny", d.Verdict)
	}
}

// TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003: what a later call adds to is the input
// after the last enter or ctrl+c, so a line split across calls is decided whole.
func TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003(t *testing.T) {
	for target, want := range map[string]string{
		"rm -":                     "rm -",
		"ls<enter>rm -":            "rm -",
		"a<enter>":                 "",
		"x<ctrl+c>rm":              "rm",
		"a<<enter>b":               "a<<enter>b",
		"q<up><backspace>":         "q<up><backspace>",
		"SELECT 1;<enter><ctrl+d>": "<ctrl+d>",
	} {
		if got := ProgramTail(target); got != want {
			t.Errorf("ProgramTail(%q) = %q, want %q", target, got, want)
		}
	}
	if d := Decide(programInput(ProgramTail("ls<enter>rm -")+"rf ~<enter>"), ModeNormal, []Rule{allowRule("terminal_interact", "*")}, false); d.Reason != ReasonDestructive {
		t.Errorf("a line split across calls: %s %q, want destructive", d.Verdict, d.Reason)
	}
}
