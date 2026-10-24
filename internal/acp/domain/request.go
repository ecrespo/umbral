package domain

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// The agent's requests of Umbral that a turn decides and records (REQ-ACP-004, REQ-ACP-005).
const (
	MethodReadFile  = "fs/read_text_file"
	MethodWriteFile = "fs/write_text_file"
	MethodTerminal  = "terminal/create"
)

// Request is one of the agent's requests of Umbral: a file read or write by its absolute Path
// (a write with its Content, for the approval's diff), or a terminal's Command line in Cwd with
// its PolicyLine, which the policy decides as well.
type Request struct {
	Method  string
	Path    string
	Content string
	Command string
	Policy  string
	// Script marks a terminal whose words hold shell syntax of their own (InterpretsScript).
	Script bool
	Cwd    string
}

// Input is what a tool call's raw input names that a permission is decided on (delta
// `2026-10-acp-mediation`, decision 2): an execute's command line and directory, a fetch's URL.
type Input struct {
	Command string
	Cwd     string
	URL     string
}

// ParseInput reads raw input's `command` (a string, or an array of strings joined with
// spaces), `cwd` or else `workdir`, and `url`. What is missing or of another shape is "".
func ParseInput(raw []byte) Input {
	var in struct {
		Command json.RawMessage `json:"command"`
		Cwd     any             `json:"cwd"`
		Workdir any             `json:"workdir"`
		URL     any             `json:"url"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return Input{}
	}
	out := Input{Cwd: str(in.Cwd), URL: str(in.URL)}
	if out.Cwd == "" {
		out.Cwd = str(in.Workdir)
	}
	var one string
	var many []string
	switch {
	case json.Unmarshal(in.Command, &one) == nil:
		out.Command = one
	case json.Unmarshal(in.Command, &many) == nil:
		out.Command = strings.Join(many, " ")
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// plainWord is a word a POSIX shell reads as itself.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// Quote makes s one word for a POSIX shell: bash or zsh, never fish, whose single quotes read
// `\'` as an escape.
func Quote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// CommandLine is a terminal's command line (decision 7): command and args quoted, env as an
// `env NAME=value` prefix, which the destructive list reads through like any wrapper.
func CommandLine(command string, args []string, env [][2]string) string {
	words := make([]string, 0, 2+len(env)+len(args))
	if len(env) > 0 {
		words = append(words, "env")
		for _, kv := range env {
			words = append(words, Quote(kv[0]+"="+kv[1]))
		}
	}
	words = append(words, Quote(command))
	for _, a := range args {
		words = append(words, Quote(a))
	}
	return strings.Join(words, " ")
}

// policyBreaks are the characters the policy's command normaliser splits a line at or strips,
// without reading quotes: whitespace, its separators, quotes and the backslash.
var policyBreaks = regexp.MustCompile("[\\s;&|`(){}'\"\\\\]")

// PolicyLine is a terminal's line as the policy's normaliser must read it: one token per word
// the shell will see, each such character inside a word made `_`. The normaliser splits on
// spaces and separators without reading quotes, so the quoted line alone could hide the program
// — `env 'A=x y' rm -rf /` reads as the command `y` — from the destructive floor (delta
// `2026-10-acp-mediation`, decision 7).
func PolicyLine(command string, args []string, env [][2]string) string {
	words := make([]string, 0, 2+len(env)+len(args))
	if len(env) > 0 {
		words = append(words, "env")
		for _, kv := range env {
			words = append(words, policyBreaks.ReplaceAllString(kv[0]+"="+kv[1], "_"))
		}
	}
	for _, w := range append([]string{command}, args...) {
		words = append(words, policyBreaks.ReplaceAllString(w, "_"))
	}
	return strings.Join(words, " ")
}

// scriptProgram matches a program that runs a script it is given — every POSIX and C shell by
// name, with a version or none, fish, xonsh, elvish, nu, busybox, PowerShell, cmd, ssh (whose
// remote command is one), eval, and the runners whose operand is a shell line (watch, su,
// script, flock) — in any case, `.exe` or not, whatever its flags: a terminal naming one asks,
// so no flag grammar has to be right. Like the destructive list it guards, it is a floor, not a
// sandbox: what any program does is outside it, and a real shell tokenizer is T-HRD-02's.
var scriptProgram = regexp.MustCompile(`(?i)^((ba|z|da|a|k|mk|pdk|ya|po|rba|rk|tc|c)?sh[0-9.]*|fish|xonsh|elvish|nu|busybox|pwsh|powershell|cmd|ssh|eval|watch|su|script|flock)(\.exe)?$`)

// safeEnvOption is an `env` option that cannot make env read a script, compared whole: anything
// else — `-S`, `--split-string`, a bundle, an abbreviation — is taken for one.
func safeEnvOption(w string) bool {
	switch w {
	case "-i", "-0", "-u", "--ignore-environment", "--null":
		return true
	}
	return strings.HasPrefix(w, "--unset=")
}

// InterpretsScript reports whether a terminal's words may hold shell syntax of their own, which
// the command normaliser reads without its quotes: any word naming a program that runs a script
// (scriptProgram), or `env` followed by any option but the safe ones. It reads no flag grammar
// and skips no word, so it fails closed; the terminal then asks (delta `2026-10-acp-mediation`,
// decision 7).
func InterpretsScript(command string, args []string) bool {
	env := false
	for _, w := range append([]string{command}, args...) {
		base := filepath.Base(w)
		switch {
		case scriptProgram.MatchString(base):
			return true
		case base == "env":
			env = true
		case env && strings.HasPrefix(w, "-") && !safeEnvOption(w):
			return true
		}
	}
	return false
}

// CheckTerminal refuses a terminal whose command, arguments or environment hold a control
// character. The line is typed into an interactive shell, whose line editor acts on one even
// inside quotes — Ctrl-U erases what came before it — so the line the policy decided would not
// be the line that runs (delta `2026-10-acp-mediation`, decision 7).
func CheckTerminal(command string, args []string, env [][2]string) error {
	words := append([]string{command}, args...)
	for _, kv := range env {
		words = append(words, kv[0], kv[1])
	}
	for _, w := range words {
		if strings.IndexFunc(w, unicode.IsControl) >= 0 {
			return fmt.Errorf("%q holds a control character, which the shell's line editor would act on", w)
		}
	}
	return nil
}

// ValidEnvName reports whether name can be a terminal's variable.
func ValidEnvName(name string) bool { return envName.MatchString(name) }

// TerminalExit is how a terminal's command ended: its exit code when the shell reported one.
type TerminalExit struct {
	Code *int
}
