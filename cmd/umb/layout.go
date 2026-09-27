package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The `umb layout` half of the tree commands (REQ-CLI-006): what a layout looks like to a
// person, and how `apply --from` reads one back. The command table is in workspace.go with
// the rest, so the usage and the dispatch stay one list.

// layoutApplyParams reads the layout named by --from (REQ-CLI-006).
//
// The file is what `umb layout export --json` wrote: a whole Layout, of which only the tree
// is the portable part — the workspace, the tab and the pane identifiers it names belong to
// the tab it was exported from. A bare node is accepted too, since that is the `root` the
// method takes and a hand-written layout may well be just that.
func layoutApplyParams(pos []string, extra map[string]any) (map[string]any, error) {
	from, _ := extra[fromKey].(string)
	delete(extra, fromKey)
	if from == "" {
		return nil, fmt.Errorf("--from is required: a file, or - for stdin")
	}

	var data []byte
	var err error
	if from == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		//nolint:gosec // the user names the file on their own command line; reading it is the point
		data, err = os.ReadFile(from)
	}
	if err != nil {
		return nil, fmt.Errorf("read the layout: %w", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the layout is not a JSON object: %w", err)
	}
	root, ok := doc["root"]
	if !ok {
		if _, isNode := doc["type"]; !isNode {
			return nil, fmt.Errorf("the layout has no root: expected what `umb layout export --json` writes")
		}
		root = json.RawMessage(data)
	}
	return merge(map[string]any{"workspace_id": pos[0], "root": root}, extra), nil
}

// printLayout draws the tree with its splits, for a person; `--json` is the form a script
// keeps and `layout apply` reads back.
func printLayout(out, _ *printer, layout map[string]any) {
	out.printf("layout of %s (focused %s)\n", str(layout["tab_id"]), str(layout["focused_pane_id"]))
	if root, ok := layout["root"].(map[string]any); ok {
		printNode(out, root, "  ")
	}
}

func printNode(out *printer, node map[string]any, indent string) {
	if str(node["type"]) == "split" {
		out.printf("%ssplit %s %v\n", indent, str(node["direction"]), node["ratio"])
		for _, child := range []string{"first", "second"} {
			if c, ok := node[child].(map[string]any); ok {
				printNode(out, c, indent+"  ")
			}
		}
		return
	}
	line := indent + "pane " + str(node["pane_id"])
	if label := str(node[keyLabel]); label != "" {
		line += "  " + label
	}
	if cwd := str(node["cwd"]); cwd != "" {
		line += "  " + cwd
	}
	if cmd, ok := node["command"].([]any); ok && len(cmd) > 0 {
		parts := make([]string, len(cmd))
		for i, a := range cmd {
			parts[i] = str(a)
		}
		line += "  $ " + strings.Join(parts, " ")
	}
	out.println(line)
}

// printApplied shows the new tab and its panes, and the warnings on stderr: they are the only
// place the user learns that the commands in the new tab were typed and not run
// (REQ-WS-005, REQ-TERM-011), so a printer that dropped them would turn a safety property
// into a tab that looks broken.
func printApplied(out, errOut *printer, result map[string]any) {
	if tab, ok := result["tab"].(map[string]any); ok {
		printObject(out, famTab, tab)
	}
	if panes, ok := result["panes"].([]any); ok {
		for _, p := range panes {
			if obj, ok := p.(map[string]any); ok {
				printObject(out, famPane, obj)
			}
		}
	}
	if warnings, ok := result["warnings"].([]any); ok {
		for _, w := range warnings {
			errOut.printf("umb: note: %s\n", str(w))
		}
	}
}
