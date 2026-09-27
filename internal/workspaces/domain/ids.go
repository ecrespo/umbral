// Package domain holds the workspace tree's pure types: the public identifier grammar, the
// attention states and the rollup rule. It imports nothing outside the standard library.
package domain

import (
	"fmt"
	"regexp"
	"strconv"
)

// The public identifier grammar of REQ-WS-002: `w<n>`, `w<n>:t<m>` and `w<n>:p<m>`.
//
// These are the one exception to Art. 6's prefixed ULIDs, written into the constitution by
// the amendment of 2026-09-20, and the reason is that a person types them: `umb pane focus
// w1:p2` is an address, not an opaque handle.
//
// The patterns are stricter than the `CHECK` constraints in migration 0003, which use GLOB
// because SQLite has no regular expressions and so cannot forbid `w0` or a leading zero.
// Stricter in Go than in SQL is the safe direction — nothing this module writes can be
// rejected by the database — and it keeps `w01` and `w1` from being two names for one
// workspace.
var (
	workspacePattern = regexp.MustCompile(`^w([1-9][0-9]*)$`)
	tabPattern       = regexp.MustCompile(`^(w[1-9][0-9]*):t([1-9][0-9]*)$`)
	panePattern      = regexp.MustCompile(`^(w[1-9][0-9]*):p([1-9][0-9]*)$`)
)

// FormatWorkspaceID builds `w<n>`. Numbering starts at 1: `w0` is not in the grammar.
func FormatWorkspaceID(n int) string { return fmt.Sprintf("w%d", n) }

// FormatTabID builds `w<n>:t<m>` under an already-valid workspace id.
func FormatTabID(workspaceID string, n int) string {
	return fmt.Sprintf("%s:t%d", workspaceID, n)
}

// FormatPaneID builds `w<n>:p<m>`. Pane numbers are scoped to the workspace and not to the
// tab, which is what makes a pane's identifier survive being moved between tabs of the same
// workspace — only a move to another workspace renames it (REQ-WS-007).
func FormatPaneID(workspaceID string, n int) string {
	return fmt.Sprintf("%s:p%d", workspaceID, n)
}

// ParseWorkspaceID returns the ordinal of a workspace identifier.
func ParseWorkspaceID(id string) (int, bool) {
	m := workspacePattern.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	return atoi(m[1])
}

// ParseTabID returns the owning workspace identifier and the tab's ordinal.
func ParseTabID(id string) (workspaceID string, ordinal int, ok bool) {
	m := tabPattern.FindStringSubmatch(id)
	if m == nil {
		return "", 0, false
	}
	n, ok := atoi(m[2])
	return m[1], n, ok
}

// ParsePaneID returns the owning workspace identifier and the pane's ordinal.
func ParsePaneID(id string) (workspaceID string, ordinal int, ok bool) {
	m := panePattern.FindStringSubmatch(id)
	if m == nil {
		return "", 0, false
	}
	n, ok := atoi(m[2])
	return m[1], n, ok
}

// WorkspaceOf reports which workspace a tab or pane identifier belongs to. A workspace
// identifier belongs to itself, so callers holding any of the three can ask one question.
func WorkspaceOf(id string) (string, bool) {
	if _, ok := ParseWorkspaceID(id); ok {
		return id, true
	}
	if ws, _, ok := ParseTabID(id); ok {
		return ws, true
	}
	if ws, _, ok := ParsePaneID(id); ok {
		return ws, true
	}
	return "", false
}

// atoi guards against an ordinal too large for an int. The pattern already excludes
// everything but digits, so overflow is the only way this fails, and a 19-digit workspace
// number should be a validation error rather than a silently wrapped one.
func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
