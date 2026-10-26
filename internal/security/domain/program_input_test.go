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
	// A deny matches every reading of a ctrl+c or ctrl+d inside a line, cut there or not.
	if d := Decide(programInput("x<ctrl+c>DELETE FROM t;<enter>"), ModeNormal, deny, false); d.Verdict != VerdictDeny {
		t.Errorf("a deny after a ctrl+c: %s, want deny", d.Verdict)
	}
	if d := Decide(programInput("SELECT 1;<enter>DELETE FROM t;<enter>"), ModeNormal, deny, false); d.Verdict != VerdictDeny {
		t.Errorf("a deny matching one line: %s, want deny", d.Verdict)
	}
}

// TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003: what a later call adds to is the input
// after the last key every terminal sends as a line end, so a line split across calls is
// decided whole; ctrl+c, whose effect is the program's, does not end it.
func TestProgramTailIsTheLineNotYetEntered_REQ_FTU_003(t *testing.T) {
	for target, want := range map[string]string{
		"rm -":                     "rm -",
		"ls<enter>rm -":            "rm -",
		"a<enter>":                 "",
		"x<ctrl+c>rm":              "x<ctrl+c>rm",
		"rm -r<ctrl+o>":            "rm -r<ctrl+o>",
		"rm -r<ctrl+d>":            "rm -r<ctrl+d>",
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

// TestProgramInputKeysTheMatcherDoesNotModelAsk_REQ_FTU_003: every key that ends a line ends
// one for the matcher, a space is a space, and a key that can bring back, complete or move
// within text the matcher does not see asks like a destructive pattern, never as `always`. A
// line piped to a shell or one the parser cannot read asks too: neither is a query's glob.
func TestProgramInputKeysTheMatcherDoesNotModelAsk_REQ_FTU_003(t *testing.T) {
	all := []Rule{allowRule("terminal_interact", "*")}
	for _, c := range []struct {
		keys   string
		rules  []Rule
		reason string
	}{
		{"rm<space>-rf<space>/<enter>", all, ReasonDestructive},
		{"SELECT 1<ctrl+j>DELETE FROM t<enter>", []Rule{allowRule("terminal_interact", "SELECT*")}, ReasonPolicy},
		{"SELECT 1<ctrl+m>DELETE FROM t<enter>", []Rule{allowRule("terminal_interact", "SELECT*")}, ReasonPolicy},
		{"<up><enter>", all, ReasonDestructive},
		{"ls<tab><enter>", all, ReasonDestructive},
		{"<ctrl+y><enter>", all, ReasonDestructive},
		{"<ctrl+r>rm<enter>", all, ReasonDestructive},
		{"<escape>k<enter>", all, ReasonDestructive},
		{"m -rf /<home>r<enter>", all, ReasonDestructive},
		{"cat f | bash<enter>", all, ReasonDestructive},
		{"echo 'a<enter>", all, ReasonDestructive},
		{"ls |<enter>", all, ReasonDestructive},
		{"rm -r\\<enter>f ~<enter>", all, ReasonDestructive},
		{"x<ctrl+d>rm -rf ~<enter>", all, ReasonDestructive},
		{"x<ctrl+c>rm -rf ~<enter>", all, ReasonDestructive},
		{"DROP TA<ctrl+d>BLE x;<enter>", all, ReasonDestructive},
		{"DROP TA<ctrl+c>BLE x;<enter>", all, ReasonDestructive},
		{"SELECT 1<ctrl+o><enter>", all, ReasonDestructive},
		{"SELECT count(*) FROM t WHERE id IN (1, 2);<enter>", all, ""},
		{"SELECT 1;<enter><ctrl+d>", all, ""},
		{"x<ctrl+c>", all, ""},
		{"<ctrl+c>SELECT 1;<enter>", all, ""},
		{"x<ctrl+c><enter>SELECT 1;<enter>", all, ""},
		{"SELECT 1;<ctrl+d><enter>", all, ""},
		{"DROP<ctrl+c>x<ctrl+d> TABLE t;<enter>", all, ReasonDestructive},
		{"rm -r<ctrl+d><backspace><enter>", all, ReasonDestructive},
		{"SELECT 2<backspace>1;<ctrl+h><enter>", []Rule{allowRule("terminal_interact", "SELECT 1")}, ""},
	} {
		d := Decide(programInput(c.keys), ModeNormal, c.rules, false)
		if d.Reason != c.reason {
			t.Errorf("%q: %s %q, want %q", c.keys, d.Verdict, d.Reason, c.reason)
		}
		if c.reason == ReasonDestructive && d.AllowAlways {
			t.Errorf("%q: offered always", c.keys)
		}
	}
	for target, want := range map[string]string{
		"a<ctrl+j>rm": "rm",
		"a<ctrl+m>rm": "rm",
	} {
		if got := ProgramTail(target); got != want {
			t.Errorf("ProgramTail(%q) = %q, want %q", target, got, want)
		}
	}
}

// TestProgramPendingIsTextAnInterruptLeft_REQ_FTU_003: the text a program was typed and not
// sent an interrupt after is what the shell may read once the program ends.
func TestProgramPendingIsTextAnInterruptLeft_REQ_FTU_003(t *testing.T) {
	for target, want := range map[string]bool{
		"":              false,
		"<ctrl+c>":      false,
		"x<ctrl+c>":     false,
		"<ctrl+c>y":     true,
		"rm -":          true,
		"<<":            true,
		"<space>":       true,
		"<up><ctrl+d>":  false,
		"a<ctrl+c><up>": false,
	} {
		if got := ProgramPending(target); got != want {
			t.Errorf("ProgramPending(%q) = %v, want %v", target, got, want)
		}
	}
}
