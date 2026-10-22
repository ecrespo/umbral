// Package domain is the domain layer of the context module, which assembles what a turn's
// prompt carries besides the conversation: rules files, attachments and git state (REQ-CTX-*).
// It imports nothing outside the standard library (Art. 3).
package domain

import (
	"errors"
	"path/filepath"
	"strings"
)

// rulesNames are the rules files REQ-CTX-001 names, in their precedence at equal depth.
var rulesNames = []string{"AGENTS", "CLAUDE", "WARP", "CRUSH"}

// RulesFile is one rules file found for a cwd.
type RulesFile struct {
	Path    string
	Content string
	// TruncatedBytes is how much of the file was left out past MaxAttachmentBytes.
	TruncatedBytes int64
}

// RulesCandidates lists every path a rules file may have for a cwd, highest precedence
// first (REQ-CTX-001): directories from the cwd up to root, the closest first, and within
// each AGENTS → CLAUDE → WARP → CRUSH with each `.local.md` above its own file. A root that
// is not the cwd or one of its ancestors searches the cwd alone.
func RulesCandidates(root, cwd string) []string {
	root, cwd = filepath.Clean(root), filepath.Clean(cwd)
	if !within(root, cwd) {
		root = cwd
	}
	var out []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, name := range rulesNames {
			out = append(out, filepath.Join(dir, name+".local.md"), filepath.Join(dir, name+".md"))
		}
		if dir == root || filepath.Dir(dir) == dir {
			return out
		}
	}
}

// within reports whether path is root or lies below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// GitContext is REQ-CTX-003's git state of a cwd inside a repository.
type GitContext struct {
	Root     string
	Branch   string
	Status   string
	DiffStat string
	// Unavailable says why git could not be read in a repository — a timeout, an ownership
	// refusal — and the prompt states it instead of the sections.
	Unavailable string
}

// BlockText is a block as an attachment shows it: its command, how it ended and its output
// as plain text.
type BlockText struct {
	ID       string
	Command  string
	ExitCode *int
	Plain    string
}

// ErrUnknownAttachment is an attachment that names nothing this kind can attach: a missing
// file, a directory given as a file, an unknown block. The API maps it to VALIDATION_ERROR.
var ErrUnknownAttachment = errors.New("unknown attachment")
