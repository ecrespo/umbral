package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ecrespo/umbral/internal/client"
)

// The workspace tree from the command line: `umb workspace`, `tab`, `pane` and `layout`
// (REQ-CLI-005, REQ-CLI-006, delta `2026-09-cli-workspace-surface`).
//
// Every command is one call to the JSON-RPC method of the same name and nothing else
// (decision 1). The CLI keeps no model of the tree, resolves no identifier and composes no
// calls: `umb pane split w1:p3` sends `w1:p3`, and whether that is a live pane or an alias of
// a moved one (REQ-WS-007) is the daemon's to say (DD-001). Objects are addressed by their
// public identifiers, positionally (decision 2), because the command line is the addressing
// surface Art. 6's exception for `w<n>` identifiers is written for.

// The four families and their subcommands. Named once, because the same words are the
// command line, the method names and the identifiers' kinds.
const (
	famWorkspace = "workspace"
	famTab       = "tab"
	famPane      = "pane"
	famLayout    = "layout"

	subCreate = "create"
	subList   = "list"
	subFocus  = "focus"
	subRename = "rename"
	subClose  = "close"
	subSplit  = "split"
	subGet    = "get"
	subExport = "export"
	subApply  = "apply"

	keyLabel = "label"
)

// treeCommand is one subcommand of a family: the positional arguments it takes, and how its
// flags and positionals become the parameters of the method it calls. The method is never
// written down: it is `<family>.<subcommand>`, which is decision 1 of the delta spelled as
// code — a command whose name and method disagreed could not be expressed here.
type treeCommand struct {
	// args names the positional arguments in order, for usage and for the arity check.
	// A trailing "?" marks the optional ones.
	args  []string
	about string
	// flags registers the subcommand's own flags; nil when it has none.
	flags func(fs *flag.FlagSet) func() (map[string]any, error)
	// takesCommand accepts a command after `--`; only `pane split` runs one.
	takesCommand bool
	// params builds the method's parameters from the positionals and the flag values.
	params func(pos []string, extra map[string]any) (map[string]any, error)
	// human prints the result for a person; --json prints it verbatim instead.
	human func(out, errOut *printer, result map[string]any)
}

// treeFamilies is the whole surface, and the usage text is generated from it, so a command
// cannot exist without being listed or be listed without existing.
func treeFamilies() map[string]map[string]treeCommand {
	id := func(key string) func([]string, map[string]any) (map[string]any, error) {
		return func(pos []string, extra map[string]any) (map[string]any, error) {
			return merge(map[string]any{key: pos[0]}, extra), nil
		}
	}
	rename := func(key string) func([]string, map[string]any) (map[string]any, error) {
		return func(pos []string, _ map[string]any) (map[string]any, error) {
			return map[string]any{key: pos[0], keyLabel: pos[1]}, nil
		}
	}

	return map[string]map[string]treeCommand{
		famWorkspace: {
			subCreate: {
				args:  []string{"cwd?"},
				about: "a workspace with one tab and one pane; cwd defaults to this directory",
				flags: func(fs *flag.FlagSet) func() (map[string]any, error) {
					label := fs.String(keyLabel, "", "the workspace's label")
					tabLabel := fs.String("tab-label", "", "the first tab's label")
					noFocus := fs.Bool("no-focus", false, "do not focus the new workspace")
					return func() (map[string]any, error) {
						return optional(map[string]any{keyLabel: *label, "tab_label": *tabLabel}, *noFocus), nil
					}
				},
				params: func(pos []string, extra map[string]any) (map[string]any, error) {
					dir := "."
					if len(pos) > 0 {
						dir = pos[0]
					}
					// The daemon's working directory is not this shell's, so a relative path
					// would name somewhere else by the time it arrives.
					abs, err := filepath.Abs(dir)
					if err != nil {
						return nil, fmt.Errorf("resolve %q: %w", dir, err)
					}
					return merge(map[string]any{"cwd": abs}, extra), nil
				},
				human: printTree,
			},
			subList:   {about: "every open workspace", params: none, human: printTree},
			subFocus:  {args: []string{famWorkspace}, about: "focus a workspace", params: id("workspace_id"), human: printTree},
			subRename: {args: []string{famWorkspace, keyLabel}, about: "relabel a workspace", params: rename("workspace_id"), human: printTree},
			subClose:  {args: []string{famWorkspace}, about: "close a workspace and its panes", params: id("workspace_id"), human: printClosed},
		},
		famTab: {
			subCreate: {
				args: []string{famWorkspace}, about: "a tab with one pane",
				flags: func(fs *flag.FlagSet) func() (map[string]any, error) {
					label := fs.String(keyLabel, "", "the tab's label")
					noFocus := fs.Bool("no-focus", false, "do not focus the new tab")
					return func() (map[string]any, error) {
						return optional(map[string]any{keyLabel: *label}, *noFocus), nil
					}
				},
				params: id("workspace_id"), human: printTree,
			},
			subList:   {args: []string{famWorkspace}, about: "a workspace's tabs", params: id("workspace_id"), human: printTree},
			subFocus:  {args: []string{famTab}, about: "focus a tab", params: id("tab_id"), human: printTree},
			subRename: {args: []string{famTab, keyLabel}, about: "relabel a tab", params: rename("tab_id"), human: printTree},
			subClose:  {args: []string{famTab}, about: "close a tab and its panes", params: id("tab_id"), human: printClosed},
		},
		famPane: {
			subSplit: {
				args:  []string{famPane},
				about: "split a pane; a command after -- runs instead of a shell", takesCommand: true,
				flags: func(fs *flag.FlagSet) func() (map[string]any, error) {
					direction := fs.String("direction", "right", "right or down")
					ratio := fs.Float64("ratio", 0, "the first child's share, 0.1-0.9 (default 0.5)")
					cwd := fs.String("cwd", "", "the new pane's directory (default: the split pane's)")
					noFocus := fs.Bool("no-focus", false, "do not focus the new pane")
					return func() (map[string]any, error) {
						p := optional(map[string]any{"direction": *direction}, *noFocus)
						// Sent whenever it was given, zero included: a ratio outside
						// 0.1-0.9 is the daemon's to reject, not the CLI's to swallow.
						fs.Visit(func(f *flag.Flag) {
							if f.Name == "ratio" {
								p["ratio"] = *ratio
							}
						})
						if *cwd != "" {
							abs, err := filepath.Abs(*cwd)
							if err != nil {
								return nil, fmt.Errorf("resolve --cwd %q: %w", *cwd, err)
							}
							p["cwd"] = abs
						}
						return p, nil
					}
				},
				params: id("pane_id"), human: printTree,
			},
			subList:   {args: []string{famTab}, about: "a tab's panes", params: id("tab_id"), human: printTree},
			subGet:    {args: []string{famPane}, about: "one pane", params: id("pane_id"), human: printTree},
			subFocus:  {args: []string{famPane}, about: "focus a pane", params: id("pane_id"), human: printTree},
			subRename: {args: []string{famPane, keyLabel}, about: "relabel a pane", params: rename("pane_id"), human: printTree},
			subClose:  {args: []string{famPane}, about: "close a pane and end its session", params: id("pane_id"), human: printClosed},
		},
		famLayout: {
			subExport: {
				args:  []string{"tab?"},
				about: "a tab's layout as a portable tree; --json is the form `apply` reads",
				params: func(pos []string, _ map[string]any) (map[string]any, error) {
					if len(pos) == 0 {
						return map[string]any{}, nil // the focused tab (API Spec §5.7)
					}
					return map[string]any{"tab_id": pos[0]}, nil
				},
				human: printLayout,
			},
			subApply: {
				args:  []string{famWorkspace},
				about: "a new tab from a layout; --from FILE, or - for stdin (required)",
				flags: func(fs *flag.FlagSet) func() (map[string]any, error) {
					from := fs.String("from", "", "the layout to apply: a file, or - for stdin")
					tabLabel := fs.String("tab-label", "", "the new tab's label")
					noFocus := fs.Bool("no-focus", false, "do not focus the new tab")
					return func() (map[string]any, error) {
						p := optional(map[string]any{"tab_label": *tabLabel}, *noFocus)
						p[fromKey] = *from
						return p, nil
					}
				},
				params: layoutApplyParams,
				human:  printApplied,
			},
		},
	}
}

// fromKey carries --from from the flag set to layoutApplyParams. It is not a parameter of
// `layout.apply` and never reaches the daemon.
const fromKey = "\x00from"

// cmdTree runs one command of a tree family: `umb <family> <subcommand> [args] [flags]`.
func cmdTree(ctx context.Context, family string, args []string, stdout, stderr *printer) int {
	commands := treeFamilies()[family]
	if len(args) == 0 {
		stderr.printf("umb %s: expected a subcommand\n\n", family)
		familyUsage(stderr, family, commands)
		return exitFailure
	}
	sub, ok := commands[args[0]]
	if !ok {
		// Rejected before connecting: a typo must not reach the daemon, let alone be sent
		// as a call it has to refuse.
		stderr.printf("umb %s: unknown subcommand %q\n\n", family, args[0])
		familyUsage(stderr, family, commands)
		return exitFailure
	}

	name := "umb " + family + " " + args[0]
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	extra := func() (map[string]any, error) { return nil, nil }
	if sub.flags != nil {
		extra = sub.flags(fs)
	}

	pos, command, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitFailure
	}
	if msg := checkArity(sub.args, pos); msg != "" {
		stderr.printf("%s: %s\nusage: %s\n", name, msg, commandLine(family, args[0], sub))
		return exitFailure
	}
	if len(command) > 0 && !sub.takesCommand {
		stderr.printf("%s: takes no command after --\n", name)
		return exitFailure
	}

	method := family + "." + args[0]
	flagParams, err := extra()
	if err != nil {
		stderr.printf("%s: %v\n", name, err)
		return exitFailure
	}
	params, err := sub.params(pos, flagParams)
	if err != nil {
		stderr.printf("%s: %v\n", name, err)
		return exitFailure
	}
	if len(command) > 0 {
		params["command"] = command
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var raw json.RawMessage
	if err := c.Call(callCtx, method, params, &raw); err != nil {
		stderr.printf("umb: %s: %v\n", method, describeError(err))
		return exitFailure
	}

	if f.asJSON {
		// Verbatim: the daemon's answer is the contract (API Spec §4), and re-encoding it
		// through a struct here would drop whatever field this build does not know yet.
		return printJSON(stdout, stderr, raw)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		stderr.printf("umb: %s answered something that is not an object: %v\n", method, err)
		return exitFailure
	}
	sub.human(stdout, stderr, result)
	return exitOK
}

// parseInterleaved accepts positionals before, between and after flags —
// `umb pane split w1:p1 --direction down` as well as `umb pane split --direction down w1:p1`
// — which the standard library's parser does not: it stops at the first non-flag. Everything
// after a bare `--` is a command for `pane split`, never a positional.
func parseInterleaved(fs *flag.FlagSet, args []string) (pos, command []string, err error) {
	for i, a := range args {
		if a == "--" {
			args, command = args[:i], args[i+1:]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, command, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// checkArity reports what is wrong with the positionals, or "" when they fit.
func checkArity(spec, pos []string) string {
	required := 0
	for _, a := range spec {
		if !strings.HasSuffix(a, "?") {
			required++
		}
	}
	switch {
	case len(pos) < required:
		return fmt.Sprintf("missing %s", strings.Join(spec[len(pos):required], ", "))
	case len(pos) > len(spec):
		return fmt.Sprintf("unexpected argument %q", pos[len(spec)])
	}
	return ""
}

func familyUsage(p *printer, family string, commands map[string]treeCommand) {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	p.printf("Usage of umb %s:\n", family)
	for _, name := range names {
		p.printf("  %-44s %s\n", commandLine(family, name, commands[name]), commands[name].about)
	}
	p.print("\nEvery command takes --json, --socket, --daemon-path and --no-autostart.\n")
}

func commandLine(family, name string, c treeCommand) string {
	parts := []string{"umb", family, name}
	for _, a := range c.args {
		if strings.HasSuffix(a, "?") {
			parts = append(parts, "["+strings.TrimSuffix(a, "?")+"]")
		} else {
			parts = append(parts, "<"+a+">")
		}
	}
	return strings.Join(parts, " ")
}

func none([]string, map[string]any) (map[string]any, error) { return map[string]any{}, nil }

// optional drops the empty strings, so an omitted flag is an omitted parameter and the
// daemon's default applies, and adds `focus: false` only when asked.
func optional(m map[string]any, noFocus bool) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	if noFocus {
		out["focus"] = false
	}
	return out
}

func merge(base, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// describeError is the daemon's own message, with its domain code when it has one, so a
// script's stderr says NOT_FOUND rather than only a sentence.
func describeError(err error) string {
	if code := client.DomainCode(err); code != "" && !strings.Contains(err.Error(), code) {
		return fmt.Sprintf("%v (%s)", err, code)
	}
	return err.Error()
}

// printTree is the human form of any answer made of workspaces, tabs and panes: one line per
// object, identifier first, because the identifier is what the next command needs.
func printTree(out, _ *printer, result map[string]any) {
	for _, key := range []string{famWorkspace, famTab, "root_pane", famPane} {
		if obj, ok := result[key].(map[string]any); ok {
			printObject(out, key, obj)
		}
	}
	if items, ok := result["items"].([]any); ok {
		if len(items) == 0 {
			out.println("(none)")
		}
		for _, item := range items {
			if obj, ok := item.(map[string]any); ok {
				printObject(out, kindOf(obj), obj)
			}
		}
	}
	if layout, ok := result["layout"].(map[string]any); ok {
		printLayout(out, nil, layout)
	}
}

func printObject(out *printer, kind string, obj map[string]any) {
	if kind == "root_pane" {
		kind = famPane
	}
	line := fmt.Sprintf("%-9s %s", kind, str(obj["id"]))
	if label := str(obj[keyLabel]); label != "" {
		line += "  " + label
	}
	if cwd := str(obj["cwd"]); cwd != "" {
		line += "  " + cwd
	}
	out.println(line)
}

// kindOf tells a list item's kind from the identifier's shape, which REQ-WS-002 fixes:
// `w<n>`, `w<n>:t<m>`, `w<n>:p<m>`.
func kindOf(obj map[string]any) string {
	id := str(obj["id"])
	_, rest, found := strings.Cut(id, ":")
	switch {
	case !found:
		return famWorkspace
	case strings.HasPrefix(rest, "t"):
		return famTab
	default:
		return famPane
	}
}

func printClosed(out, _ *printer, result map[string]any) {
	if closed, _ := result["closed"].(bool); closed {
		out.println("closed")
		return
	}
	out.println("not closed")
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
