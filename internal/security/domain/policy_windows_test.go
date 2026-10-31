package domain

import "testing"

// TestADenyRuleHoldsWhateverThePathsSpelling_REQ_WIN_001 is decision 15 of the Windows delta: NTFS
// opens C:\Secrets\key.txt by any case and either separator, so a deny rule matches them all.
// Matched as written, `c:/SECRETS/key.txt` would step around `C:\secrets\*`.
func TestADenyRuleHoldsWhateverThePathsSpelling_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	rules := []Rule{{Tool: "*", Pattern: `C:\secrets\*`, Decision: VerdictDeny}}
	for _, target := range []string{`C:\secrets\key.txt`, `c:\SECRETS\key.txt`, `c:/secrets/key.txt`, `C:/Secrets\KEY.TXT`} {
		d := Decide(Action{Tool: "write_file", Risk: RiskWriteFS, Target: target, Cwd: `C:\repo`, WriteRoot: `C:\repo`}, ModeAutoEdit, rules, false)
		if d.Verdict != VerdictDeny || d.Reason != ReasonDenyRule {
			t.Errorf("write %s = %s/%q by %s, want deny by the rule", target, d.Verdict, d.Reason, d.Step)
		}
	}

	// The write root is compared the same way, so auto-edit inside it does not ask.
	d := Decide(Action{Tool: "write_file", Risk: RiskWriteFS, Target: `c:\REPO\main.go`, Cwd: `C:\repo`, WriteRoot: `C:\Repo`}, ModeAutoEdit, nil, false)
	if d.Verdict != VerdictAllow {
		t.Errorf("a write inside the write root spelled in another case = %s/%q, want allow", d.Verdict, d.Reason)
	}
}

// TestEveryCommandAsksOnWindows_REQ_WIN_001 is decision 14: the destructive floor reads POSIX
// shell lines, and an agent's shell on Windows is PowerShell or cmd, so no line is run unasked —
// not by full-auto inside the worktree, not by an allow rule — and none is remembered `always`.
// A deny rule still refuses.
func TestEveryCommandAsksOnWindows_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	inside := Action{Tool: "run_command", Risk: RiskExec, Target: "git status", Cwd: `C:\wt\thr_1`, WriteRoot: `C:\wt\thr_1`}
	if d := Decide(inside, ModeFullAuto, nil, false); d.Verdict != VerdictAsk || d.Step != StepDestructive || d.AllowAlways {
		t.Errorf("full-auto inside its worktree = %+v, want an ask by the destructive step, never always", d)
	}
	allowed := []Rule{{Tool: "run_command", Pattern: "git status*", Decision: VerdictAllow}}
	if d := Decide(inside, ModeAutoEdit, allowed, false); d.Verdict != VerdictAsk {
		t.Errorf("an allowed command = %s, want ask", d.Verdict)
	}
	if name, ok := MatchDestructive(DefaultDestructivePatterns, "Get-ChildItem"); !ok || name != UnknownWindowsShell {
		t.Errorf("MatchDestructive(Get-ChildItem) = %q, %v; want %q", name, ok, UnknownWindowsShell)
	}
	if _, ok := MatchDestructive(DefaultDestructivePatterns, "   "); ok {
		t.Error("an empty line asks")
	}
	denied := []Rule{{Tool: "run_command", Pattern: "Remove-Item*", Decision: VerdictDeny}}
	if d := Decide(Action{Tool: "run_command", Risk: RiskExec, Target: "Remove-Item -Recurse C:\\"}, ModeAutoEdit, denied, false); d.Verdict != VerdictDeny {
		t.Errorf("a denied command = %s, want deny", d.Verdict)
	}
}

// TestAWriteIsDecidedByItsDrivePath_REQ_WIN_001: the write root and the target are Windows
// paths with a drive, and auto-edit and full-auto act on them as on a POSIX tree: inside the
// root a write runs, outside it asks, and a relative target is read under the call's cwd.
func TestAWriteIsDecidedByItsDrivePath_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	repos := map[string]bool{`C:\u\project`: true}
	isRepo := func(dir string) bool { return repos[dir] }
	for cwd, want := range map[string]string{
		`C:\u\project`:              `C:\u\project`,
		`C:\u\project\internal\api`: `C:\u\project`,
		`C:\u\notes`:                `C:\u\notes`,
		`D:\`:                       `D:\`,
	} {
		if got := WriteRoot(cwd, isRepo); got != want {
			t.Errorf("WriteRoot(%s) = %s, want %s", cwd, got, want)
		}
	}

	root := `C:\u\project`
	write := func(target string) Action {
		return Action{ThreadID: "thr_1", Tool: "edit_file", Risk: RiskWriteFS, Target: target, Cwd: root, WriteRoot: root}
	}
	for _, mode := range []Mode{ModeAutoEdit, ModeFullAuto} {
		for target, want := range map[string]Verdict{
			`C:\u\project\main.go`:      VerdictAllow,
			`main.go`:                   VerdictAllow,
			`C:\u\other\main.go`:        VerdictAsk,
			`D:\u\project\main.go`:      VerdictAsk,
			`C:\u\project\..\x\main.go`: VerdictAsk,
			`..\x\main.go`:              VerdictAsk,
		} {
			if d := Decide(write(target), mode, nil, false); d.Verdict != want {
				t.Errorf("%s: write %s = %s/%q by %s, want %s", mode, target, d.Verdict, d.Reason, d.Step, want)
			}
		}
	}
}

// TestAProgramsInputAsksOnWindows_REQ_WIN_001: keys sent to a program are also read as the
// shell's next line when the program does not take them (delta `2026-10-terminal-interact`,
// decision 4); on Windows that shell's lines are not read, so a line of program input asks
// as a command line does (decision 14). Keys with no line in them stay the program's.
func TestAProgramsInputAsksOnWindows_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	input := Action{Tool: "terminal_interact", Risk: RiskExec, Target: ":wq<enter>", ProgramInput: true, Cwd: `C:\wt`, WriteRoot: `C:\wt`}
	if d := Decide(input, ModeFullAuto, nil, false); d.Verdict != VerdictAsk || d.Step != StepDestructive {
		t.Errorf("program input in full-auto = %s/%q by %s, want an ask by the destructive step", d.Verdict, d.Reason, d.Step)
	}
}
