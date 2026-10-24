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

// TestACPKindsMapToRisks_REQ_ACP_003: every ACP kind maps to REQ-ACP-003's risk; `fetch` is
// Network, and a kind ACP lacks is Exec, never looser than a command.
func TestACPKindsMapToRisks_REQ_ACP_003(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]string{
		"edit": "WriteFS", "delete": "WriteFS", "move": "WriteFS",
		"execute": "Exec", "fetch": "Network",
		"read": "ReadOnly", "search": "ReadOnly", "think": "ReadOnly",
		"other": "Exec", "switch_mode": "Exec", "": "Exec",
	} {
		if got := string(Risk(Kind(kind))); got != want {
			t.Errorf("kind %q is %s, want %s", kind, got, want)
		}
	}
}

// TestACallsInputNamesItsTargets_REQ_ACP_003: an execute's command (a string, or an array
// joined), its cwd or workdir, and a fetch's url are read from the raw input; anything else
// is no target.
func TestACallsInputNamesItsTargets_REQ_ACP_003(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]Input{
		`{"command":"go test ./...","cwd":"/r"}`:       {Command: "go test ./...", Cwd: "/r"},
		`{"command":["git","status"],"workdir":"sub"}`: {Command: "git status", Cwd: "sub"},
		`{"url":"https://example.com/"}`:               {URL: "https://example.com/"},
		`{"command":42}`:                               {},
		`not json`:                                     {},
		``:                                             {},
	} {
		if got := ParseInput([]byte(raw)); got != want {
			t.Errorf("ParseInput(%s) = %+v, want %+v", raw, got, want)
		}
	}
}

// TestATerminalsCommandLineIsQuoted_REQ_ACP_005: a terminal's command and args are quoted for
// a POSIX shell, so an argument is one word whatever it holds, and env is an `env` prefix the
// destructive list sees through.
func TestATerminalsCommandLineIsQuoted_REQ_ACP_005(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		cmd  string
		args []string
		env  [][2]string
		want string
	}{
		{"go", []string{"test", "./..."}, nil, "go test ./..."},
		{"echo", []string{"a b", "it's", "$HOME", ""}, nil, `echo 'a b' 'it'"'"'s' '$HOME' ''`},
		{"make", nil, [][2]string{{"CGO_ENABLED", "0"}, {"X", "a b"}}, `env CGO_ENABLED=0 'X=a b' make`},
	} {
		if got := CommandLine(c.cmd, c.args, c.env); got != c.want {
			t.Errorf("CommandLine(%q, %q, %q) = %s, want %s", c.cmd, c.args, c.env, got, c.want)
		}
	}
}

// TestATerminalsControlCharactersAreRefused_REQ_ACP_005: the line is typed into an interactive
// shell, whose line editor acts on a control character even inside quotes — Ctrl-U erases what
// came before it — so the line the policy decided would not be the one that runs.
func TestATerminalsControlCharactersAreRefused_REQ_ACP_005(t *testing.T) {
	for name, tc := range map[string]struct {
		command string
		args    []string
		env     [][2]string
	}{
		"erase in an arg":   {"echo", []string{"\x15rm -rf ~ #"}, nil},
		"start of line":     {"echo\x01", nil, nil},
		"tab completes":     {"echo", []string{"a\tb"}, nil},
		"delete":            {"echo", []string{"a\x7f"}, nil},
		"in an env value":   {"echo", nil, [][2]string{{"A", "x\x15y"}}},
		"in an env name":    {"echo", nil, [][2]string{{"A\x01", "x"}}},
		"a newline":         {"echo", []string{"a\nrm -rf ~"}, nil},
		"a C1 control byte": {"echo", []string{"a\u009bb"}, nil},
	} {
		if err := CheckTerminal(tc.command, tc.args, tc.env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := CheckTerminal("printf", []string{"héllo wörld", "it's"}, [][2]string{{"GREETING", "a b"}}); err != nil {
		t.Errorf("a plain terminal was refused: %v", err)
	}
}

// TestATerminalsPolicyLineKeepsItsWords_REQ_ACP_005: the policy's command normaliser splits on
// spaces and separators without reading quotes, so the policy line has one token per word the
// shell will see — the program stays the program, whatever a quoted word holds.
func TestATerminalsPolicyLineKeepsItsWords_REQ_ACP_005(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		env     [][2]string
		want    string
	}{
		{"rm", []string{"-rf", "/"}, [][2]string{{"A", "x y"}}, "env A=x_y rm -rf /"},
		{"rm", []string{"x;y", "-rf", "/"}, nil, "rm x_y -rf /"},
		{"git", []string{"commit", "-m", `it's "done" (a|b) && \ok`}, nil, "git commit -m it_s__done___a_b______ok"},
		{"printf", []string{"plain"}, nil, "printf plain"},
	} {
		if got := PolicyLine(tc.command, tc.args, tc.env); got != tc.want {
			t.Errorf("PolicyLine(%q, %q, %q) = %q, want %q", tc.command, tc.args, tc.env, got, tc.want)
		}
	}
}

// TestATerminalThatRunsAScriptIsFlagged_REQ_ACP_005: a word that is itself shell syntax — a
// shell's `-c` script, `eval`'s, `env -S`'s — is read by the command normaliser without its
// quotes, so the policy cannot see what it runs; such a terminal is flagged, so it asks.
func TestATerminalThatRunsAScriptIsFlagged_REQ_ACP_005(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		want    bool
	}{
		{"sh", []string{"-c", "env 'A=x y' rm -rf /"}, true},
		{"/bin/bash", []string{"-lc", "make"}, true},
		{"bash", []string{"script.sh"}, true},
		{"env", []string{"-S", "rm -rf /"}, true},
		{"env", []string{"--split-string=rm -rf /"}, true},
		{"env", []string{"-iS", "rm -rf /"}, true},
		{"env", []string{"--split", "rm -rf /"}, true},
		{"env", []string{"-u", "-S", "rm -rf /"}, true},
		{"env", []string{"-u", "X", "sh", "-u", "-c", "x"}, true},
		{"sudo", []string{"zsh", "-c", "x"}, true},
		{"eval", []string{"rm -rf /"}, true},
		{"xargs", []string{"sh", "-c", "x"}, true},
		{"busybox", []string{"sh", "-c", "x"}, true},
		{"/usr/bin/mksh", []string{"-c", "x"}, true},
		{"ash", []string{"-ec", "x"}, true},
		{"sh5", []string{"-c", "x"}, true},
		{"tcsh", []string{"-c", "x"}, true},
		{"fish", []string{"--command=x"}, true},
		{"pwsh", []string{"-File", "x.ps1"}, true},
		{"powershell", []string{"/c", "x"}, true},
		{"ssh", []string{"host", "rm", "-rf", "/"}, true},
		{"BASH.EXE", []string{"-c", "x"}, true},
		{"pwsh.exe", []string{"x"}, true},
		{"watch", []string{"rm -rf /"}, true},
		{"su", []string{"-c", "x"}, true},
		{"script", []string{"-c", "x"}, true},
		{"flock", []string{"/tmp/l", "-c", "x"}, true},
		{"xonsh", []string{"-c", "x"}, true},
		{"nu", []string{"-c", "x"}, true},
		{"env", []string{"-i", "-u", "HOME", "A=b", "make"}, false},
		{"env", []string{"--unset=HOME", "make"}, false},
		{"go", []string{"test", "./..."}, false},
		{"git", []string{"commit", "-m", "fix bash"}, false},
		{"git", []string{"push", "origin", "main"}, false},
		{"npm", []string{"run", "publish"}, false},
		{"make", []string{"shell", "refresh"}, false},
	} {
		if got := InterpretsScript(tc.command, tc.args); got != tc.want {
			t.Errorf("InterpretsScript(%q, %q) = %v, want %v", tc.command, tc.args, got, tc.want)
		}
	}
}
