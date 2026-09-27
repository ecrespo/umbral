package domain

import (
	"path/filepath"
	"regexp"
	"strings"
)

// DestructivePattern is one entry of the destructive-command list (REQ-SEC-005). Expr is
// matched against each simple command of a command line after it is normalised: split at
// `;`, `&&`, `||`, `|`, `&`, newlines, backticks, parentheses and braces (so `$(…)` too), stripped of quotes, of leading
// assignments and of wrappers such as sudo or env, with the program reduced to its base
// name, and its words joined by single spaces. `sh -c '…'` is normalised again as a command
// line of its own.
type DestructivePattern struct {
	Name string
	Expr *regexp.Regexp
}

// DefaultDestructivePatterns is the list built into the binary; a rule bundle replaces it
// (REQ-SEC-010, T-F1-30).
var DefaultDestructivePatterns = []DestructivePattern{
	{"rm_recursive", regexp.MustCompile(`^rm( \S+)* (-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)( |$)`)},
	{"git_push_force", regexp.MustCompile(`^git( \S+)* push( \S+)* (-f|--force|--force-with-lease(=\S*)?|--mirror|\+\S+)( |$)`)},
	{"git_reset_hard", regexp.MustCompile(`^git( \S+)* reset( \S+)* --hard( |$)`)},
	{"git_clean_force", regexp.MustCompile(`^git( \S+)* clean( \S+)* (-[a-zA-Z]*f[a-zA-Z]*|--force)( |$)`)},
	{"mkfs", regexp.MustCompile(`^mkfs(\.\S+)?( |$)`)},
	{"dd_of", regexp.MustCompile(`^dd( \S+)* of=`)},
	{"kubectl_delete", regexp.MustCompile(`^kubectl( \S+)* delete( |$)`)},
	{"terraform_destroy", regexp.MustCompile(`^terraform( \S+)* (destroy|apply( \S+)* -destroy)( |$)`)},
	{"helm_uninstall", regexp.MustCompile(`^helm( \S+)* (uninstall|delete)( |$)`)},
	{"container_prune", regexp.MustCompile(`^(docker|podman)( \S+)* (system prune|volume (rm|prune))( |$)`)},
	{"find_delete", regexp.MustCompile(`^find( \S+)* -delete( |$)`)},
	{"shred", regexp.MustCompile(`^(shred|wipefs)( |$)`)},
	{"power", regexp.MustCompile(`^(shutdown|reboot|halt|poweroff)( |$)`)},
	{"sql_drop", regexp.MustCompile(`(?i)\bdrop (table|database|schema)\b`)},
	{"block_device_write", regexp.MustCompile(`(^| )>+ ?/dev/(sd|hd|vd|xvd|nvme|mmcblk|disk)`)},
	{"recursive_chmod_root", regexp.MustCompile(`^(chmod|chown)( \S+)* (-[a-zA-Z]*R[a-zA-Z]*|--recursive)( \S+)* /$`)},
}

// MatchDestructive reports the first pattern that matches any simple command of the line.
func MatchDestructive(patterns []DestructivePattern, command string) (string, bool) {
	for _, simple := range simpleCommands(command, 0) {
		for _, p := range patterns {
			if p.Expr.MatchString(simple) {
				return p.Name, true
			}
		}
	}
	return "", false
}

var separators = regexp.MustCompile("&&|\\|\\||[;|&\\n`(){}]")

// wrapper is a program that runs the command after it: flags lists the flags that take an
// argument (skipped with it), and positional counts the operands before the command
// (timeout's duration, chroot's root).
type wrapper struct {
	flags      map[string]bool
	positional int
}

var wrappers = map[string]wrapper{
	"sudo":     {flags: map[string]bool{"-u": true, "-g": true, "-h": true, "-p": true, "-C": true, "-D": true, "-r": true, "-t": true, "-U": true}},
	"doas":     {flags: map[string]bool{"-u": true, "-C": true}},
	"env":      {flags: map[string]bool{"-u": true, "-C": true, "-S": true}},
	"xargs":    {flags: map[string]bool{"-n": true, "-I": true, "-L": true, "-P": true, "-d": true, "-s": true, "-E": true, "-a": true}},
	"nice":     {flags: map[string]bool{"-n": true}},
	"ionice":   {flags: map[string]bool{"-c": true, "-n": true, "-p": true}},
	"stdbuf":   {flags: map[string]bool{"-i": true, "-o": true, "-e": true}},
	"timeout":  {flags: map[string]bool{"-k": true, "-s": true}, positional: 1},
	"chroot":   {positional: 1},
	"watch":    {flags: map[string]bool{"-n": true}},
	"parallel": {flags: map[string]bool{"-j": true}},
	"nohup":    {},
	"time":     {},
	"command":  {},
	"exec":     {},
	"builtin":  {},
	"eval":     {},
	"busybox":  {},
}

// findExec are find's actions whose operands are a command.
var findExec = map[string]bool{"-exec": true, "-execdir": true, "-ok": true, "-okdir": true}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

// simpleCommands normalises a command line into its simple commands. depth bounds the
// recursion through `sh -c`.
func simpleCommands(line string, depth int) []string {
	var out []string
	for _, part := range separators.Split(line, -1) {
		words := strings.Fields(strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(part))
		words = unwrap(words)
		if len(words) == 0 {
			continue
		}
		words[0] = filepath.Base(words[0])
		if words[0] == "find" && depth < 3 {
			if inner, ok := findCommand(words); ok {
				out = append(out, strings.Join(words, " "))
				out = append(out, simpleCommands(inner, depth+1)...)
				continue
			}
		}
		if shells[words[0]] && depth < 3 {
			if inner, ok := shellCommand(words); ok {
				out = append(out, simpleCommands(inner, depth+1)...)
				continue
			}
		}
		out = append(out, strings.Join(words, " "))
	}
	return out
}

// unwrap drops leading `VAR=value` assignments and wrappers with their flags.
func unwrap(words []string) []string {
	for len(words) > 0 {
		switch w := words[0]; {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "="):
			words = words[1:]
		case isWrapper(w):
			wr := wrappers[filepath.Base(w)]
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				flag := words[0]
				words = words[1:]
				if wr.flags[flag] && len(words) > 0 {
					words = words[1:]
				}
			}
			for n := 0; n < wr.positional && len(words) > 0; n++ {
				words = words[1:]
			}
		default:
			return words
		}
	}
	return words
}

func isWrapper(word string) bool {
	_, ok := wrappers[filepath.Base(word)]
	return ok
}

// findCommand finds the command of `find … -exec cmd …`; the `{}` and the `;` or `+` that end
// it are already gone, split off as separators.
func findCommand(words []string) (string, bool) {
	for i, w := range words {
		if findExec[w] && i+1 < len(words) {
			return strings.Join(words[i+1:], " "), true
		}
	}
	return "", false
}

// commandParts splits a command line at its separators, as simpleCommands does, but keeps
// each part as written — rules match what the user would type, wrappers included.
func commandParts(line string) []string {
	var out []string
	for _, part := range separators.Split(line, -1) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// shellCommand finds the script of `sh -c script` (or a flag cluster holding c, `-lc`); with
// quotes already stripped, the script is every word after the flag.
func shellCommand(words []string) (string, bool) {
	for i, w := range words[1:] {
		if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "c") {
			return strings.Join(words[i+2:], " "), true
		}
	}
	return "", false
}
