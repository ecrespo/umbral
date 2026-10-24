package domain

import (
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// DestructivePattern is one entry of the destructive-command list (REQ-SEC-005). Expr is
// matched against each simple command of a command line after it is normalised: the line is
// parsed as bash parses it, so quotes, escapes and separators mean what they mean to the
// shell and every command is found, inside `$(…)` too; each simple command loses its
// assignments and wrappers such as sudo, env or setsid, its program is reduced to its base
// name, and its words, with any output redirection, are joined by single spaces. A script the
// line hands to a shell (`sh -c '…'`, eval, a here-document, ssh's command) is normalised
// again as a command line of its own (delta `2026-10-shell-parser`).
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
	// The agent must not change what instructs it or which tools it has: a skill installed or
	// an MCP server added from inside a turn would persist a prompt injection (REQ-SKL-005,
	// deltas `2026-09-skills-cli` and `2026-09-cli-mcp`).
	{"umb_skill_change", regexp.MustCompile(`^umb( \S+)* skill( \S+)* (install|enable|remove)( |$)`)},
	{"umb_mcp_add", regexp.MustCompile(`^umb( \S+)* mcp( \S+)* add( |$)`)},
	// Nor land or drop its own worktree: a merge is the user's review of the diff (delta
	// `2026-10-worktree-engine`, decision 19), and full-auto runs commands in the worktree.
	{"umb_worktree_change", regexp.MustCompile(`^umb( \S+)* worktree( \S+)* (merge|discard)( |$)`)},
	// Nor change the rules that decide for it, or drop a server the user added. The daemon
	// refuses both to an agent's terminal; these patterns ask for what escapes that check
	// (delta `2026-10-rules-mcp-patterns`, Q2 and Q6 of `2026-10-hrd02-open-questions`).
	{"umb_rules_change", regexp.MustCompile(`^umb( \S+)* rules( \S+)* (key (add|rotate|remove)|rollback|reset)( |$)`)},
	{"umb_mcp_remove", regexp.MustCompile(`^umb( \S+)* mcp( \S+)* remove( |$)`)},
}

// What makes a line ask when no pattern names it: the shell, not the line, decides what runs
// (delta `2026-10-shell-parser`).
const (
	// UnknownDynamicWord is a word built at run time — a variable, a command, process or
	// arithmetic substitution, a brace expansion, a glob, `$'…'` — or arguments taken from
	// stdin (xargs, parallel) or split from a string (`env -S`).
	UnknownDynamicWord = "dynamic_word"
	// UnknownStdinScript is a shell, at, batch or ssh reading its commands from stdin, which the
	// line does not show unless it is a here-document or here-string.
	UnknownStdinScript = "stdin_script"
	// UnknownUnparseable is a line the parser cannot read, or scripts nested too deep.
	UnknownUnparseable = "unparseable"
)

// maxScriptDepth bounds how many scripts inside scripts are read; deeper asks.
const maxScriptDepth = 3

// MatchDestructive reports the first pattern that matches any simple command of the line;
// failing that, the reason the line cannot be read statically, if there is one. Either way
// the line asks.
func MatchDestructive(patterns []DestructivePattern, command string) (string, bool) {
	n := &normaliser{}
	n.script(command, 0)
	for _, simple := range n.commands {
		for _, p := range patterns {
			if p.Expr.MatchString(simple) {
				return p.Name, true
			}
		}
	}
	if n.unknown != "" {
		return n.unknown, true
	}
	return "", false
}

var separators = regexp.MustCompile("&&|\\|\\||[;|&\\n`(){}]")

// wrapper is a program that runs the command after it: flags lists the flags that take an
// argument (skipped with it), noArg the ones known to take none, and positional counts the
// operands before the command (timeout's duration, chroot's root, ssh's host). A flag in
// neither list is read both ways, as taking an argument and as not, so a flag the table lacks
// cannot hide the command. script marks a wrapper that hands its words to a shell as one
// script (eval, watch, ssh); fromStdin one that appends arguments read from stdin (xargs,
// parallel).
type wrapper struct {
	flags      map[string]bool
	noArg      map[string]bool
	positional int
	script     bool
	fromStdin  bool
	// shell lists the flags that make the wrapper start a shell when no command follows.
	shell map[string]bool
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var wrappers = map[string]wrapper{
	"sudo": {
		flags: set("-u", "--user", "-g", "--group", "-h", "--host", "-p", "--prompt", "-C", "--close-from", "-D", "--chdir", "-r", "--role", "-t", "--type", "-U", "--other-user", "-T", "--command-timeout", "-R", "--chroot"),
		noArg: set("-A", "--askpass", "-b", "--background", "-E", "--preserve-env", "-e", "--edit", "-H", "--set-home", "-i", "--login", "-K", "--remove-timestamp", "-k", "--reset-timestamp", "-l", "--list", "-n", "--non-interactive", "-P", "--preserve-groups", "-S", "--stdin", "-s", "--shell", "-V", "--version", "-v", "--validate"),
		shell: set("-s", "--shell", "-i", "--login"),
	},
	"doas":    {flags: set("-u", "-C"), noArg: set("-n", "-s", "-L"), shell: set("-s")},
	"pkexec":  {flags: set("--user"), noArg: set("--disable-internal-agent", "--keep-cwd")},
	"taskset": {noArg: set("-c", "--cpu-list", "-a", "--all-tasks"), positional: 1},
	"unshare": {
		flags: set("-S", "--setuid", "-G", "--setgid", "-R", "--root", "-w", "--wd", "--propagation", "--setgroups"),
		noArg: set("-m", "--mount", "-u", "--uts", "-i", "--ipc", "-n", "--net", "-p", "--pid", "-U", "--user", "-C", "--cgroup",
			"-T", "--time", "-f", "--fork", "-r", "--map-root-user", "-c", "--map-current-user", "--mount-proc", "--kill-child"),
	},
	"nsenter": {
		flags: set("-t", "--target", "-S", "--setuid", "-G", "--setgid"),
		noArg: set("-m", "--mount", "-u", "--uts", "-i", "--ipc", "-n", "--net", "-p", "--pid", "-U", "--user", "-C", "--cgroup",
			"-T", "--time", "-a", "--all", "-F", "--no-fork", "-r", "--root", "-w", "--wd", "--preserve-credentials"),
	},
	"strace": {
		flags: set("-o", "--output", "-p", "-e", "-s", "-u", "-E", "-P", "-I", "-b", "-a", "-O", "-S", "-X"),
		noArg: set("-f", "-ff", "-c", "-C", "-d", "-D", "-F", "-h", "-i", "-k", "-q", "-qq", "-r", "-t", "-tt", "-ttt", "-T",
			"-v", "-V", "-w", "-x", "-xx", "-y", "-yy", "-z", "-Z"),
	},
	"ltrace": {flags: set("-o", "--output", "-p", "-e", "-s", "-u", "-a", "-n", "-l"), noArg: set("-f", "-c", "-C", "-i", "-S", "-t", "-tt", "-ttt", "-T")},
	// zsh's precommand modifiers.
	"noglob":    {},
	"nocorrect": {},
	"env":       {flags: set("-u", "--unset", "-C", "--chdir", "-S", "--split-string"), noArg: set("-i", "--ignore-environment", "-0", "--null", "-v", "--debug")},
	"xargs":     {flags: set("-n", "--max-args", "-I", "-L", "--max-lines", "-P", "--max-procs", "-d", "--delimiter", "-s", "--max-chars", "-E", "-a", "--arg-file"), noArg: set("-0", "--null", "-r", "--no-run-if-empty", "-t", "--verbose", "-p", "--interactive", "-x", "--exit", "-o", "--open-tty"), fromStdin: true},
	"nice":      {flags: set("-n", "--adjustment")},
	"ionice":    {flags: set("-c", "--class", "-n", "--classdata", "-p", "--pid", "-P", "--pgid", "-u", "--uid"), noArg: set("-t", "--ignore")},
	"stdbuf":    {flags: set("-i", "--input", "-o", "--output", "-e", "--error")},
	"timeout":   {flags: set("-k", "--kill-after", "-s", "--signal"), noArg: set("--preserve-status", "--foreground", "-v", "--verbose"), positional: 1},
	"chroot":    {flags: set("--userspec", "--groups"), noArg: set("--skip-chdir"), positional: 1},
	"watch":     {flags: set("-n", "--interval", "-q", "--equexit"), noArg: set("-d", "--differences", "-t", "--no-title", "-b", "--beep", "-e", "--errexit", "-g", "--chgexit", "-c", "--color", "-x", "--exec", "-p", "--precise", "-w", "--no-wrap", "-r", "--no-rerun"), script: true},
	"parallel":  {flags: set("-j", "--jobs"), fromStdin: true},
	"nohup":     {},
	"time":      {noArg: set("-p", "-v", "--verbose", "-a", "--append", "-q", "--quiet"), flags: set("-f", "--format", "-o", "--output")},
	"command":   {noArg: set("-p", "-v", "-V")},
	"exec":      {flags: set("-a"), noArg: set("-c", "-l")},
	"builtin":   {},
	"eval":      {script: true},
	"busybox":   {},
	// Launchers that start the command outside the caller's session (Q1 of delta
	// `2026-10-hrd02-open-questions`).
	"setsid": {noArg: set("-c", "--ctty", "-f", "--fork", "-w", "--wait")},
	"systemd-run": {
		flags: set("-p", "--property", "-E", "--setenv", "-u", "--unit", "-M", "--machine", "-H", "--host",
			"--description", "--slice", "--uid", "--gid", "--nice", "--working-directory", "--service-type",
			"--on-active", "--on-boot", "--on-startup", "--on-unit-active", "--on-unit-inactive",
			"--on-calendar", "--timer-property", "--path-property", "--socket-property", "--json", "--slice-inherit"),
		noArg: set("--user", "--system", "--scope", "-t", "--pty", "-P", "--pipe", "-q", "--quiet", "-G", "--collect",
			"--wait", "-S", "--shell", "-d", "--same-dir", "--no-block", "-r", "--remain-after-exit", "--no-ask-password", "--send-sighup"),
	},
	sshProgram: {
		flags: set("-B", "-b", "-c", "-D", "-E", "-e", "-F", "-I", "-i", "-J", "-L", "-l", "-m", "-O", "-o", "-P", "-p",
			"-Q", "-R", "-S", "-W", "-w"),
		noArg:      set("-4", "-6", "-A", "-a", "-C", "-f", "-G", "-g", "-K", "-k", "-M", "-N", "-n", "-q", "-s", "-T", "-t", "-V", "-v", "-X", "-x", "-Y", "-y"),
		positional: 1, script: true,
	},
}

// sshProgram reads its command after the host as a script, and options after the host too.
const sshProgram = "ssh"

// findExec are find's actions whose operands are a command.
var findExec = map[string]bool{"-exec": true, "-execdir": true, "-ok": true, "-okdir": true}

var shells = set("sh", "bash", "rbash", "zsh", "dash", "ksh", "mksh", "oksh", "ash", "yash", "posh", "fish", "csh", "tcsh")

// normaliser collects a line's simple commands as the patterns read them, and the first
// reason, if any, that the line cannot be read statically.
type normaliser struct {
	commands []string
	unknown  string
}

func (n *normaliser) flag(why string) {
	if n.unknown == "" {
		n.unknown = why
	}
}

// script parses src as bash does and normalises every simple command in it, at any depth of
// the syntax tree: in pipelines, lists, subshells, functions, loops and substitutions.
func (n *normaliser) script(src string, depth int) {
	if depth > maxScriptDepth {
		n.flag(UnknownUnparseable)
		return
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		n.flag(UnknownUnparseable)
		return
	}
	syntax.Walk(f, func(node syntax.Node) bool {
		switch x := node.(type) {
		case *syntax.Stmt:
			if call, ok := x.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
				n.call(call, x.Redirs, depth)
			} else if out := n.outputs(x.Redirs); len(out) > 0 {
				// A block, a subshell, a loop or a bare redirection writes too.
				n.commands = append(n.commands, strings.Join(out, " "))
			}
		case *syntax.Assign:
			n.assign(x, depth)
		case *syntax.ArithmCmd, *syntax.LetClause, *syntax.ArithmExp, *syntax.CStyleLoop:
			// Arithmetic evaluates a variable's value as an expression, and a subscript in it
			// runs a substitution: `x='a[$(cmd)]'; (( x ))`.
			n.flag(UnknownDynamicWord)
		case *syntax.BinaryTest:
			if arithmeticTests[x.Op] {
				n.flag(UnknownDynamicWord)
			}
		}
		return true
	})
}

var arithmeticTests = map[syntax.BinTestOperator]bool{
	syntax.TsEql: true, syntax.TsNeq: true, syntax.TsLeq: true, syntax.TsGeq: true, syntax.TsLss: true, syntax.TsGtr: true,
}

// promptVariables hold code the interactive shell runs at every prompt.
var promptVariables = set("PS0", "PS1", "PS2", "PS3", "PS4", "PROMPT_COMMAND", "PROMPT", "RPROMPT", "RPS1", "PROMPT2", "PROMPT3", "PROMPT4")

// assign reads an assignment to a prompt variable as a script: the thread's shell runs it.
func (n *normaliser) assign(a *syntax.Assign, depth int) {
	if a.Name == nil || !promptVariables[a.Name.Value] {
		return
	}
	if a.Value == nil {
		if a.Array != nil {
			n.flag(UnknownDynamicWord)
		}
		return
	}
	if value, ok := staticWord(a.Value); ok {
		n.script(value, depth+1)
		// Prompt expansion runs `$(…)` written inside single quotes too.
		return
	}
	n.flag(UnknownDynamicWord)
}

// outputs renders a statement's output redirections as `> target`.
func (n *normaliser) outputs(redirs []*syntax.Redirect) []string {
	var out []string
	for _, r := range redirs {
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
			out = append(out, ">", n.word(r.Word))
		case syntax.DplOut:
			// `>& file` writes to a file; `>&2` and `>&-` name a descriptor.
			if target := n.word(r.Word); strings.Trim(target, "0123456789-") != "" {
				out = append(out, ">", target)
			}
		}
	}
	return out
}

// call renders a simple command's words and output redirections, then normalises it.
func (n *normaliser) call(call *syntax.CallExpr, redirs []*syntax.Redirect, depth int) {
	words := make([]string, 0, len(call.Args)+2)
	for _, w := range call.Args {
		words = append(words, n.word(w))
	}
	if len(words) > 0 && strings.HasPrefix(words[0], "^") {
		// `^old^new` repeats the last command, changed: history the line does not show.
		n.flag(UnknownDynamicWord)
	}
	n.command(words, n.outputs(redirs), redirs, depth)
}

// word is w's value as the shell would pass it; a word built at run time flags the line and
// stands as `$`.
func (n *normaliser) word(w *syntax.Word) string {
	if s, ok := staticWord(w); ok {
		return s
	}
	n.flag(UnknownDynamicWord)
	return "$"
}

// command normalises one simple command: wrappers and assignments are dropped, the program
// is reduced to its base name, and a command or script it runs is normalised in turn.
func (n *normaliser) command(words, out []string, redirs []*syntax.Redirect, depth int) {
	words, scripted := n.unwrap(words, out, redirs, depth)
	if scripted {
		return
	}
	if len(words) == 0 {
		if len(out) > 0 { // `exec > /dev/sda`
			n.commands = append(n.commands, strings.Join(out, " "))
		}
		return
	}
	words[0] = filepath.Base(words[0])
	n.commands = append(n.commands, strings.Join(append(append([]string(nil), words...), out...), " "))
	switch prog := words[0]; {
	case prog == "find":
		if inner, ok := findCommand(words); ok {
			if depth >= maxScriptDepth {
				n.flag(UnknownUnparseable)
				return
			}
			n.command(inner, nil, nil, depth+1)
		}
	case shells[prog]:
		n.shell(words, redirs, depth)
	case prog == "at" || prog == "batch":
		n.at(words, redirs, depth)
	case prog == "su" || prog == "runuser" || prog == "script" || prog == "flock" || prog == "trap":
		n.carrier(words, redirs, depth)
	case prog == "source" || prog == ".":
		if len(words) > 1 && stdinPath(words[1]) {
			n.stdin(redirs, depth)
		}
	case interpreter(prog):
		n.inline(words, depth)
		if encodedProgram(words) {
			n.flag(UnknownDynamicWord) // PowerShell's base64 program is not text to read
		}
		if readsProgramFromStdin(words) {
			n.stdinWith(redirs, func(src string) { n.inline([]string{prog, src}, depth) })
		}
	case prog == "fc" || prog == "r" || prog == "hash":
		// fc and r run commands from history; `hash -p` renames a program.
		n.flag(UnknownDynamicWord)
	case prog == "complete" || prog == "compgen":
		if v, ok := optionValue(words, "-C"); ok {
			n.script(v, depth+1)
		}
	case prog == "bind":
		if v, ok := optionValue(words, "-x"); ok {
			if _, cmd, found := strings.Cut(v, ":"); found {
				n.script(strings.TrimSpace(cmd), depth+1)
			} else {
				n.flag(UnknownDynamicWord)
			}
		}
	case subscripted[prog]:
		// These evaluate an array subscript in an argument, which may hold a substitution.
		for _, w := range words[1:] {
			if strings.Contains(w, "$(") || strings.Contains(w, "`") {
				n.flag(UnknownDynamicWord)
			}
		}
	case prog == "tmux" || prog == "screen":
		if !multiplexerQuery(words) {
			// They run commands, or type keys, in a terminal the line does not show.
			n.flag(UnknownDynamicWord)
		}
	case prog == "crontab":
		if len(words) == 1 || words[len(words)-1] == "-" {
			n.flag(UnknownStdinScript)
		}
	case prog == "alias":
		for _, def := range words[1:] {
			if _, value, ok := strings.Cut(def, "="); ok {
				n.script(value, depth+1)
			}
		}
	}
}

// subscripted are builtins that evaluate an array subscript written in an argument.
var subscripted = set("unset", "declare", "typeset", "local", "export", "readonly", "read", "printf", "test", "[", "mapfile", "readarray", "wait")

// multiplexerQuery reports a tmux or screen call that only lists or attaches.
func multiplexerQuery(words []string) bool {
	if len(words) < 2 {
		return words[0] == "tmux" // tmux alone opens a session the user types into
	}
	switch words[1] {
	case "ls", "list-sessions", "list-windows", "list-panes", "has-session", "has", "attach", "attach-session", "a", "-ls", "-list", "-r":
		return true
	}
	return false
}

// optionValue is the word after flag, or the rest of a word that starts with it.
func optionValue(words []string, flag string) (string, bool) {
	for i, w := range words {
		if w == flag && i+1 < len(words) {
			return words[i+1], true
		}
		if strings.HasPrefix(w, flag) && len(w) > len(flag) {
			return w[len(flag):], true
		}
	}
	return "", false
}

// unwrap drops leading `VAR=value` words and wrappers with their flags. A wrapper that hands
// its words to a shell has them normalised as a script, and scripted reports it.
func (n *normaliser) unwrap(words, out []string, redirs []*syntax.Redirect, depth int) (rest []string, scripted bool) {
	for len(words) > 0 {
		w := words[0]
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "=") {
			words = words[1:]
			continue
		}
		name := filepath.Base(w)
		wr, ok := wrappers[name]
		if !ok {
			return words, false
		}
		if wr.fromStdin {
			n.flag(UnknownDynamicWord)
		}
		shell := false
		words = words[1:]
		for len(words) > 0 && strings.HasPrefix(words[0], "-") && words[0] != "-" {
			flag := words[0]
			words = words[1:]
			if flag == "--" {
				break
			}
			if name == "env" && (strings.HasPrefix(flag, "--split-string") || (!strings.HasPrefix(flag, "--") && strings.Contains(flag, "S"))) {
				n.flag(UnknownDynamicWord)
			}
			if wr.shell[flag] {
				shell = true
			}
			words = n.wrapperFlag(name, wr, flag, words, out, redirs, depth)
		}
		for i := 0; i < wr.positional && len(words) > 0; i++ {
			words = words[1:]
		}
		if name == sshProgram {
			// OpenSSH reads options after the host too.
			for len(words) > 0 && strings.HasPrefix(words[0], "-") && words[0] != "-" {
				flag := words[0]
				words = n.wrapperFlag(name, wr, flag, words[1:], out, redirs, depth)
			}
		}
		if shell && len(words) == 0 {
			n.stdin(redirs, depth) // `sudo -s` with no command reads its commands from stdin
			return nil, true
		}
		if wr.script {
			if len(words) == 0 {
				if name == sshProgram {
					n.stdin(redirs, depth)
				}
				return nil, true
			}
			n.script(strings.Join(words, " "), depth+1)
			return nil, true
		}
	}
	return words, false
}

// wrapperFlag reads one of a wrapper's flags and returns the words after it and its value. A
// cluster of short flags (`-vo`) is read letter by letter: the first letter that takes a value
// takes the rest of the word or the next one. A letter the table does not know may take a value
// itself, so the line is also read as if the cluster ended there.
func (n *normaliser) wrapperFlag(name string, wr wrapper, flag string, words, out []string, redirs []*syntax.Redirect, depth int) []string {
	if long, value, ok := strings.Cut(flag, "="); ok && strings.HasPrefix(flag, "--") {
		if wr.flags[long] {
			n.flagValue(name, long, value, depth)
		}
		return words
	}
	switch {
	case wr.flags[flag] && len(words) > 0:
		n.flagValue(name, flag, words[0], depth)
		return words[1:]
	case wr.noArg[flag] || !shortCluster(flag) && (strings.Contains(flag, "=") || len(words) == 0):
		return words
	case shortCluster(flag):
		unknown := false
		for i := 1; i < len(flag); i++ {
			letter := "-" + flag[i:i+1]
			if !wr.flags[letter] {
				if !wr.noArg[letter] && !unknown && len(words) > 0 {
					unknown = true
					n.alternative(append([]string{name}, words[1:]...), out, redirs, depth)
				}
				continue
			}
			if value := flag[i+1:]; value != "" {
				n.flagValue(name, letter, value, depth) // `-oKey=value`, `-voKey=value`
				return words
			}
			if len(words) > 0 {
				n.flagValue(name, letter, words[0], depth)
				return words[1:]
			}
			return words
		}
		return words
	default:
		// Not in the table: also read the line as if the flag took the next word.
		n.alternative(append([]string{name}, words[1:]...), out, redirs, depth)
		return words
	}
}

// shortCluster reports one or more short flags written as one word (`-v`, `-vo`).
func shortCluster(flag string) bool {
	return len(flag) > 1 && flag[0] == '-' && flag[1] != '-'
}

// sshCommandOptions are ssh_config keywords whose value is a command ssh runs locally.
var sshCommandOptions = set("proxycommand", "localcommand", "knownhostscommand", "remotecommand")

// flagValue reads a wrapper flag's value when it holds a command: ssh's `-o ProxyCommand=…`
// and its kin, a config file (`-F`) that may hold one, and strace's or ltrace's output piped to
// a command (`-o '|cmd'`).
func (n *normaliser) flagValue(name, flag, value string, depth int) {
	switch {
	case name == sshProgram && flag == "-o":
		key, rest := sshOption(value)
		switch {
		case sshCommandOptions[key]:
			n.script(rest, depth+1)
		case key == "match" || key == "include" || strings.Trim(key, "abcdefghijklmnopqrstuvwxyz") != "":
			n.flag(UnknownDynamicWord)
		}
	case name == sshProgram && flag == "-F" && value != "/dev/null":
		n.flag(UnknownDynamicWord) // its ProxyCommand is not on the line
	case (name == "strace" || name == "ltrace") && (flag == "-o" || flag == "--output") && (strings.HasPrefix(value, "|") || strings.HasPrefix(value, "!")):
		n.script(value[1:], depth+1)
	}
}

// sshOption splits an `-o` value as OpenSSH does: the keyword ends at the first whitespace or
// `=`, then whitespace and at most one `=` are skipped. The keyword comes back lower case.
func sshOption(value string) (key, rest string) {
	value = strings.TrimLeft(value, " \t")
	end := strings.IndexAny(value, " \t\r\n=")
	if end < 0 {
		return strings.ToLower(value), ""
	}
	key, rest = value[:end], strings.TrimLeft(value[end:], " \t\r\n")
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
	}
	return strings.ToLower(key), strings.TrimSpace(rest)
}

// alternative normalises another reading of a command, one level deeper so readings cannot
// multiply without bound.
func (n *normaliser) alternative(words, out []string, redirs []*syntax.Redirect, depth int) {
	if depth >= maxScriptDepth {
		n.flag(UnknownUnparseable)
		return
	}
	n.command(words, out, redirs, depth+1)
}

// shell normalises the script of `sh -c script` (or a flag cluster holding c, `-lc`). A shell
// given a script file runs what the line does not show, as any program does; one given none,
// told -s, or given /dev/stdin, reads its commands from stdin. A long option it does not know
// may take an argument, so the line asks.
func (n *normaliser) shell(words []string, redirs []*syntax.Redirect, depth int) {
	byFlag, fromStdin := false, false
	operand := ""
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-o" || w == "+o" || w == "-O" || w == "+O" || shellArgOptions[w]:
			i++
			continue
		case w == "--" || w == "-":
			if i+1 < len(words) {
				operand = words[i+1]
			}
		case strings.HasPrefix(w, "--"):
			if !shellLongOptions[w] {
				n.flag(UnknownUnparseable)
			}
			continue
		case strings.HasPrefix(w, "-") || strings.HasPrefix(w, "+"):
			byFlag = byFlag || strings.Contains(w, "c")
			fromStdin = fromStdin || strings.Contains(w, "s")
			continue
		default:
			operand = w
		}
		break
	}
	switch {
	case byFlag:
		if operand != "" {
			n.script(operand, depth+1)
		}
	case fromStdin || operand == "" || stdinPath(operand):
		n.stdin(redirs, depth)
	}
}

// shellArgOptions are a shell's long options that take an argument; shellLongOptions those
// that take none.
var (
	shellArgOptions  = set("--rcfile", "--init-file")
	shellLongOptions = set("--login", "--noprofile", "--norc", "--posix", "--restricted", "--verbose",
		"--noediting", "--debugger", "--dump-strings", "--dump-po-strings", "--help", "--version",
		"--pretty-print", "--no-rcs", "--interactive", "--no-config", "--private")
)

// stdinPath reports a path that names the process's own stdin.
func stdinPath(p string) bool {
	return p == "/dev/stdin" || p == "-" || strings.HasPrefix(p, "/dev/fd/") || strings.HasPrefix(p, "/proc/self/fd/")
}

// carrier normalises a program that runs a script given by -c or --command (su, runuser,
// script, flock) or as its first operand (trap). Without one, su, runuser and script start a
// shell that reads stdin, and flock runs the command after its lock file.
func (n *normaliser) carrier(words []string, redirs []*syntax.Redirect, depth int) {
	prog := words[0]
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case (w == "-c" || w == "--command") && i+1 < len(words):
			n.script(words[i+1], depth+1)
			return
		case strings.HasPrefix(w, "--command="):
			n.script(strings.TrimPrefix(w, "--command="), depth+1)
			return
		}
	}
	switch prog {
	case "trap":
		for _, w := range words[1:] {
			if w == "--" || strings.HasPrefix(w, "-") {
				continue
			}
			n.script(w, depth+1)
			return
		}
	case "flock":
		rest := words[1:]
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			if flockArgOptions[rest[0]] && len(rest) > 1 {
				rest = rest[1:]
			}
			rest = rest[1:]
		}
		if len(rest) > 1 {
			n.alternative(rest[1:], nil, redirs, depth)
		}
	default:
		n.stdin(redirs, depth)
	}
}

var flockArgOptions = set("-w", "--timeout", "-E", "--conflict-exit-code")

// interpreters run a program given inline (`python -c`, `perl -e`, an awk program). The
// program is not shell, so the separator split from before the parser reads it for commands
// it might hand to a shell (`os.system("rm -rf /")`).
func interpreter(prog string) bool {
	if strings.HasPrefix(prog, "python") {
		return true
	}
	switch prog {
	case "perl", "ruby", "node", "nodejs", "bun", "deno", "php", "lua", "luajit", "awk", "gawk", "mawk", "nawk",
		"Rscript", "tclsh", "osascript", pwshProgram, powershellProgram:
		return true
	}
	return false
}

// readsProgramFromStdin reports an interpreter given no inline program, no module and no
// script file, or `-` or a stdin path as its script: it reads its program from stdin. awk's
// program is always an argument.
func readsProgramFromStdin(words []string) bool {
	prog := words[0]
	if strings.HasSuffix(prog, "awk") {
		return false
	}
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "-" || stdinPath(w):
			return true
		case inlineProgramFlag(prog, w):
			// PowerShell's `-Command -` and `-File -` read the program from stdin.
			return powershell(prog) && i+1 < len(words) && (words[i+1] == "-" || stdinPath(words[i+1]))
		case strings.HasPrefix(prog, "python") && (w == "-m" || strings.HasPrefix(w, "-m")):
			return false
		case strings.HasPrefix(prog, "python") && (w == "-W" || w == "-X"):
			i++
		case strings.HasPrefix(w, "-"):
		default:
			return false // a script file
		}
	}
	return true
}

// PowerShell takes its program inline, from a file or base64-encoded.
const (
	pwshProgram       = "pwsh"
	powershellProgram = "powershell"
)

// encodedProgram reports PowerShell's -EncodedCommand or -EncodedArguments, in any of their
// abbreviations: every parameter starting with `e` but -ExecutionPolicy's.
func encodedProgram(words []string) bool {
	if !powershell(words[0]) {
		return false
	}
	for _, w := range words[1:] {
		if p, ok := powershellParameter(w); ok && strings.HasPrefix(p, "e") && !strings.HasPrefix(p, "ex") {
			return true
		}
	}
	return false
}

func powershell(prog string) bool { return prog == pwshProgram || prog == powershellProgram }

// powershellParameter is a PowerShell parameter's name in lower case, written after `-`, `--`
// or `/`.
func powershellParameter(w string) (string, bool) {
	for _, prefix := range []string{"--", "-", "/"} {
		if name, ok := strings.CutPrefix(w, prefix); ok && name != "" {
			return strings.ToLower(name), true
		}
	}
	return "", false
}

// inlineProgramFlag reports a flag that gives an interpreter its program inline.
func inlineProgramFlag(prog, w string) bool {
	cluster := strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--")
	switch {
	case strings.HasPrefix(prog, "python"):
		return cluster && strings.Contains(w, "c")
	case prog == "perl" || prog == "ruby":
		return cluster && strings.ContainsAny(w, "eE")
	case prog == "node" || prog == "nodejs" || prog == "bun":
		return w == "-e" || w == "-p" || strings.HasPrefix(w, "--eval") || strings.HasPrefix(w, "--print")
	case prog == "deno":
		return w == "eval" || w == "run" || w == "task"
	case powershell(prog):
		p, ok := powershellParameter(w)
		return ok && (strings.HasPrefix("command", p) || strings.HasPrefix("file", p) || strings.HasPrefix(p, "e") && !strings.HasPrefix(p, "ex"))
	case prog == "php":
		return w == "-r"
	default: // lua, Rscript, osascript
		return w == "-e"
	}
}

// inline reads each argument of an interpreter as the old normaliser read a line: split at
// separators, quotes and backslashes dropped.
func (n *normaliser) inline(words []string, depth int) {
	if depth >= maxScriptDepth {
		n.flag(UnknownUnparseable)
		return
	}
	for _, w := range words[1:] {
		for _, part := range separators.Split(w, -1) {
			if fields := strings.Fields(strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(part)); len(fields) > 0 {
				n.command(fields, nil, nil, depth+1)
			}
		}
	}
}

// at and batch schedule the commands they read from stdin or, with -f, from a file; -l, -r,
// -d and -c only list, remove or show jobs.
func (n *normaliser) at(words []string, redirs []*syntax.Redirect, depth int) {
	for _, w := range words[1:] {
		switch w {
		case "-l", "-r", "-d", "-c", "-f":
			return
		}
	}
	n.stdin(redirs, depth)
}

// stdin normalises the script a here-document or here-string gives a command; without one,
// or with one built at run time, what it reads is not on the line.
func (n *normaliser) stdin(redirs []*syntax.Redirect, depth int) {
	n.stdinWith(redirs, func(src string) { n.script(src, depth+1) })
}

// stdinWith hands read the text a here-document or here-string gives a command, and flags the
// line when its stdin is something the line does not show.
func (n *normaliser) stdinWith(redirs []*syntax.Redirect, read func(string)) {
	for _, r := range redirs {
		var body *syntax.Word
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			body = r.Hdoc
		case syntax.WordHdoc:
			body = r.Word
		case syntax.RdrIn:
			target, ok := staticWord(r.Word)
			switch {
			case !ok: // `< <(…)`: a script built by a command
				n.flag(UnknownDynamicWord)
			case stdinPath(target):
				n.flag(UnknownStdinScript)
			}
			return // otherwise a file, as `sh script` is
		default:
			continue
		}
		if body == nil {
			return
		}
		src, ok := hereBody(body, r.Op == syntax.WordHdoc)
		if !ok {
			n.flag(UnknownDynamicWord)
			return
		}
		read(src)
		return
	}
	n.flag(UnknownStdinScript)
}

// hereBody is a here-document's text, or a here-string's value, when it holds nothing
// expanded at run time.
func hereBody(w *syntax.Word, isString bool) (string, bool) {
	if isString {
		return staticWord(w)
	}
	var b strings.Builder
	for _, part := range w.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		b.WriteString(lit.Value)
	}
	return b.String(), true
}

// staticWord is a word's value after quote removal, when nothing in it is expanded at run
// time: no parameter, command, process or arithmetic substitution, no `$'…'` or `$"…"`, no
// brace expansion and no unquoted glob.
func staticWord(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", true
	}
	braced := &syntax.Word{Parts: append([]syntax.WordPart(nil), w.Parts...)}
	if syntax.SplitBraces(braced) && expands(braced) {
		return "", false
	}
	var b strings.Builder
	for i, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if hasGlob(p.Value) || hasHistory(p.Value) || (i == 0 && zshEquals(p.Value)) {
				return "", false
			}
			b.WriteString(unescape(p.Value, false))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok || hasHistory(lit.Value) {
					return "", false
				}
				b.WriteString(unescape(lit.Value, true))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// expands reports a brace expansion bash performs: a sequence or two elements or more, so
// find's `{}` is a word as it is to the shell.
func expands(w *syntax.Word) bool {
	for _, part := range w.Parts {
		if b, ok := part.(*syntax.BraceExp); ok && (b.Sequence || len(b.Elems) > 1) {
			return true
		}
	}
	return false
}

// hasHistory reports a `!` the interactive shell's history expansion acts on: one not
// escaped and followed by something other than a blank, `=` or `(`.
func hasHistory(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '!':
			if i+1 < len(s) && strings.IndexByte(" \t=(", s[i+1]) < 0 {
				return true
			}
		}
	}
	return false
}

// zshEquals reports zsh's `=cmd`, which expands to the path of cmd.
func zshEquals(s string) bool {
	return len(s) > 1 && s[0] == '=' && s[1] != '=' && s[1] != ' '
}

// hasGlob reports an unescaped `*` or `?`, or a `[` closed later, in an unquoted literal.
func hasGlob(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		case '[':
			if strings.Contains(s[i+1:], "]") {
				return true
			}
		}
	}
	return false
}

// unescape removes the backslashes the shell removes: before any character outside quotes,
// and before `$`, a backquote, `"`, a backslash or a newline inside double quotes. An escaped
// newline joins the lines.
func unescape(s string, quoted bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		next := s[i+1]
		switch {
		case next == '\n':
			i++
		case !quoted || strings.IndexByte("$`\"\\", next) >= 0:
			b.WriteByte(next)
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// findCommand finds the command of `find … -exec cmd … ;`, without the `;` or `+` that ends it.
func findCommand(words []string) ([]string, bool) {
	for i, w := range words {
		if findExec[w] && i+1 < len(words) {
			inner := words[i+1:]
			for j, x := range inner {
				if x == ";" || x == "+" {
					inner = inner[:j]
					break
				}
			}
			return inner, len(inner) > 0
		}
	}
	return nil, false
}

// CommandParts is a command line's simple commands as the rules read them — each part as
// written — so a caller can tell whether a rule on the whole line would ever match again.
func CommandParts(line string) []string { return commandParts(line) }

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
