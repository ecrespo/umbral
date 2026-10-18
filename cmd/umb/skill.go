package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
)

const (
	skillInstall = "install"
	skillList    = "list"
	skillShow    = "show"
	skillEnable  = "enable"
	skillDisable = "disable"
	skillRemove  = "remove"
)

type skillInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     *string `json:"version"`
	Source      string  `json:"source"`
	SHA256      string  `json:"sha256"`
	SizeBytes   int64   `json:"size_bytes"`
	Enabled     bool    `json:"enabled"`
	InstalledAt int64   `json:"installed_at"`
}

type skillManifest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     *string `json:"version"`
	Files       []struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"files"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// skillCallTimeout bounds each skill.* call; a test shortens it.
var skillCallTimeout = callTimeout

// stdinIsTerminal reports whether the confirmation can be asked for. A test replaces it; a
// pipe, a file or nothing at all is not a terminal, and then only --yes installs.
var stdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// cmdSkill is `umb skill` (REQ-SKL-001, REQ-SKL-003, API Spec §5.37). Each subcommand is one
// skill.* method, except install, which inspects first, shows the manifest and its digest,
// and installs that digest only once the user confirmed it.
func cmdSkill(ctx context.Context, args []string, stdin io.Reader, stdout, stderr *printer) int {
	const usageText = "umb skill: install PATH [--yes] [--replace] | list | show NAME [--body] | enable NAME | disable NAME | remove NAME"
	if len(args) == 0 {
		stderr.println(usageText)
		return exitFailure
	}
	name := args[0]
	fs := flag.NewFlagSet("umb skill "+name, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	yes := fs.Bool("yes", false, "install without asking")
	replace := fs.Bool("replace", false, "replace an installed skill of the same name")
	body := fs.Bool("body", false, "print SKILL.md's body too")
	pos, more, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	pos = append(pos, more...)

	var params map[string]any
	method := map[string]string{
		skillInstall: "skill.inspect", skillList: "skill.list", skillShow: "skill.get",
		skillEnable: "skill.set_enabled", skillDisable: "skill.set_enabled", skillRemove: "skill.remove",
	}[name]
	switch {
	case name == skillList && len(pos) == 0:
		params = map[string]any{}
	case name == skillInstall && len(pos) == 1:
		params = map[string]any{"path": sourcePath(pos[0])}
	case method != "" && name != skillList && name != skillInstall && len(pos) == 1:
		params = map[string]any{"name": pos[0]}
		switch name {
		case skillShow:
			if *body {
				params["include"] = "body"
			}
		case skillEnable, skillDisable:
			params["enabled"] = name == skillEnable
		}
	default:
		stderr.println(usageText)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	// Each call has its own deadline: install's starts after the confirmation, so the time a
	// person spends reading the manifest never counts against it.
	call := func(method string, params, out any) error {
		callCtx, cancel := context.WithTimeout(ctx, skillCallTimeout)
		defer cancel()
		return c.Call(callCtx, method, params, out)
	}
	prefix := "umb skill " + name + ": "

	switch name {
	case skillInstall:
		var m skillManifest
		if err := call(method, params, &m); err != nil {
			return callFailed(stderr, prefix, err)
		}
		// With --json, stdout carries the installed Skill alone, so the manifest goes to stderr.
		shown := stdout
		if f.asJSON {
			shown = stderr
		}
		printManifest(shown, m)
		if !*yes && !confirmInstall(stdin, stderr, m) {
			return exitFailure
		}
		install := map[string]any{"path": params["path"], "expected_sha256": m.SHA256}
		if *replace {
			install["replace"] = true
		}
		var s skillInfo
		if err := call("skill.install", install, &s); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, s)
		}
		if s.Enabled {
			stdout.printf("installed %s %s\n", printable(s.Name), printable(s.SHA256))
		} else {
			stdout.printf("installed %s %s, stored disabled: %d skills are already enabled\n", printable(s.Name), printable(s.SHA256), ctxdomain.MaxEnabledSkills)
		}
	case skillList:
		var out struct {
			Items []skillInfo `json:"items"`
		}
		if err := call(method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		if len(out.Items) == 0 {
			stdout.println("no skills installed")
		}
		for _, s := range out.Items {
			stdout.printf("%s  %s  %s  %s\n", printable(s.Name), versionOf(s.Version), enabledWord(s.Enabled), printable(firstLine(s.Description, 72)))
		}
	case skillShow:
		var out struct {
			Skill skillInfo `json:"skill"`
			Body  *string   `json:"body"`
		}
		if err := call(method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		s := out.Skill
		stdout.printf("name: %s\nversion: %s\nstate: %s\ndescription: %s\nsha256: %s\nsize: %d bytes\nsource: %s\ninstalled: %s\n",
			printable(s.Name), versionOf(s.Version), enabledWord(s.Enabled), printable(s.Description), printable(s.SHA256), s.SizeBytes, printable(s.Source),
			time.UnixMilli(s.InstalledAt).UTC().Format(time.RFC3339))
		// The body is printed only when asked for, whatever the daemon sent.
		if *body && out.Body != nil {
			stdout.printf("\n%s", printableText(*out.Body))
		}
	case skillEnable, skillDisable:
		var s skillInfo
		if err := call(method, params, &s); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, s)
		}
		stdout.printf("%s: %s\n", printable(s.Name), enabledWord(s.Enabled))
	case skillRemove:
		var out map[string]any
		if err := call(method, params, &out); err != nil {
			return callFailed(stderr, prefix, err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		stdout.printf("removed %s\n", printable(pos[0]))
	}
	return exitOK
}

// sourcePath makes a local path absolute, because the daemon's working directory means
// nothing to the user. A remote source is sent as typed: resolving it would turn
// git@host:repo into a local-looking path, and the daemon's refusal (REQ-SKL-006) has to see
// it as it is.
func sourcePath(p string) string {
	if ctxdomain.IsRemoteSource(p) || filepath.IsAbs(p) {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// confirmInstall is REQ-SKL-001's confirmation: a "y" typed on a terminal. With no terminal
// to ask on, the answer is no, and the user is told to read the manifest and pass --yes.
func confirmInstall(stdin io.Reader, stderr *printer, m skillManifest) bool {
	if !stdinIsTerminal(stdin) {
		stderr.println("umb skill install: no terminal to confirm on; read the manifest above, then run again with --yes")
		return false
	}
	stderr.printf("Install %s with digest %s? [y/N] ", printable(m.Name), printable(m.SHA256))
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		stderr.println("")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	stderr.println("umb skill install: not installed")
	return false
}

func printManifest(p *printer, m skillManifest) {
	p.printf("name: %s\nversion: %s\ndescription: %s\nfiles:\n", printable(m.Name), versionOf(m.Version), printable(m.Description))
	for _, f := range m.Files {
		p.printf("  %8d  %s\n", f.SizeBytes, printable(f.Path))
	}
	p.printf("size: %d bytes in %d files\nsha256: %s\n", m.SizeBytes, len(m.Files), printable(m.SHA256))
}

func versionOf(v *string) string {
	if v == nil {
		return "-"
	}
	return printable(*v)
}

// printable escapes every rune a terminal would act on rather than show — C0 and C1 controls,
// DEL, newlines, bidi and other format characters — as Go escapes. A skill's name, paths and
// front matter come from a bundle the user has not yet trusted, and the manifest is what they
// confirm: an ESC sequence or a newline in a path must not hide a file or print a fake one.
func printable(s string) string { return escapeRunes(s, false) }

// printableText is printable for a multi-line body: newlines and tabs are kept.
func printableText(s string) string { return escapeRunes(s, true) }

func escapeRunes(s string, keepLines bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case keepLines && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case r == ' ' || unicode.IsPrint(r):
			b.WriteRune(r)
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
	}
	return b.String()
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// firstLine is a description's first line, cut to n characters for a one-line listing.
func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
