// Package domain holds the worktree types: a thread's isolated copy of a repository, the
// refusals the engine answers with, and the diff a client reviews (PRD F2 REQ-WT-001…008).
package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// State is a worktree's lifecycle state (Data Model §2.4g).
type State string

// The states a worktree row can hold.
const (
	StateActive    State = "active"
	StateMerged    State = "merged"
	StateDiscarded State = "discarded"
	StateMissing   State = "missing"
)

// Worktree is one row of `worktrees`.
type Worktree struct {
	ID           string
	ThreadID     string
	RepoRoot     string // the main working copy's top level
	RepoID       string // RepoID of the common git directory
	BaseCommit   string
	OriginBranch string // the branch a merge squashes into; "" when created on a detached HEAD
	Branch       string // BranchFor(ThreadID)
	Path         string
	State        State
	CreatedAt    int64 // UTC epoch milliseconds (Art. 6)
	UpdatedAt    int64
}

// ErrNotFound is a worktree id no row holds.
var ErrNotFound = errors.New("worktree not found")

// RepoID is the first 16 hex digits of the SHA-256 of the common git directory's real path
// (REQ-WT-001). Two working copies of one repository share it; two repositories never do.
func RepoID(commonDir string) string {
	sum := sha256.Sum256([]byte(commonDir))
	return hex.EncodeToString(sum[:])[:16]
}

// ValidThreadID reports whether id can name a worktree's branch and directory: a thread id is
// "thr_" and a ULID, so letters and digits only, never a path separator or "..".
func ValidThreadID(id string) bool {
	rest, ok := strings.CutPrefix(id, "thr_")
	if !ok || rest == "" || len(rest) > 64 {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// BranchFor is the branch a thread's worktree is created on.
func BranchFor(threadID string) string { return "umbral/" + threadID }

// The oldest git the engine runs against (constitution stack table, REQ-WT-008).
const (
	MinGitMajor = 2
	MinGitMinor = 34
)

// GitVersionSupported reads `git --version`'s output and reports whether it is MinGit or later.
// A suffix such as "(Apple Git-154)" or ".windows.1" is ignored.
func GitVersionSupported(out string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(out), "git version ")
	if !ok {
		return false
	}
	parts := strings.SplitN(strings.Fields(rest)[0], ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > MinGitMajor || (major == MinGitMajor && minor >= MinGitMinor)
}

// Reason names why the engine refused a request. API §8 carries it as `data.reason`.
type Reason string

// The refusals. The first group describes the request and is a VALIDATION_ERROR; the second
// describes the repository's state and is a CONFLICT (REQ-WT-004, REQ-WT-005, REQ-WT-008).
const (
	ReasonNotGit          Reason = "not_git"
	ReasonBare            Reason = "bare"
	ReasonOldGit          Reason = "old_git"
	ReasonNoCommit        Reason = "no_commit" // a repository with no commit has no HEAD to start from
	ReasonConfirmRequired Reason = "confirm_required"
	ReasonBadThread       Reason = "bad_thread"   // not a thread id a branch and a directory can carry
	ReasonNotInGroup      Reason = "not_in_group" // close_group named a worktree of another thread

	ReasonDirty        Reason = "dirty"
	ReasonOtherBranch  Reason = "other_branch"
	ReasonDetached     Reason = "detached"
	ReasonConflict     Reason = "conflict"
	ReasonCommitFailed Reason = "commit_failed"
	ReasonNotActive    Reason = "not_active" // merged, discarded or missing: nothing to act on
	ReasonExists       Reason = "exists"     // the thread already has an active worktree
	ReasonChanged      Reason = "changed"    // the worktree no longer holds the revision reviewed
)

// What an active worktree holds, as close_group reports it (REQ-WT-007).
const (
	ChangesChanged = "changed"
	ChangesClean   = "clean"
)

// MaxOutputBytes is how much of git's error output a commit_failed refusal carries.
const MaxOutputBytes = 4096

// Refusal is a request the engine declined, leaving every copy as it was.
type Refusal struct {
	Reason Reason
	Detail string
	Paths  []string // the conflicting paths, for ReasonConflict
	Output string   // git's error output, at most MaxOutputBytes, for ReasonCommitFailed
}

func (r *Refusal) Error() string {
	msg := "worktree: " + string(r.Reason)
	if r.Detail != "" {
		msg += ": " + r.Detail
	}
	if len(r.Paths) > 0 {
		msg += ": " + strings.Join(r.Paths, ", ")
	}
	return msg
}

// Conflict reports whether the refusal is about the repository's state rather than the request.
func (r *Refusal) Conflict() bool {
	switch r.Reason {
	case ReasonNotGit, ReasonBare, ReasonOldGit, ReasonNoCommit, ReasonConfirmRequired, ReasonBadThread, ReasonNotInGroup:
		return false
	}
	return true
}

// Clip keeps git's output within MaxOutputBytes without cutting a UTF-8 sequence in two.
func Clip(s string) string {
	if len(s) <= MaxOutputBytes {
		return s
	}
	cut := MaxOutputBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// FileDiff is one file of a worktree's diff.
type FileDiff struct {
	Path   string
	Status string // added, modified or deleted
	Binary bool   // a binary file has no hunks
	Hunks  []Hunk
}

// Hunk is one hunk of a file's diff. Its ID is stable while the hunk does not change.
type Hunk struct {
	ID     string
	Header string   // the "@@ -a,b +c,d @@" line, without the section heading git appends
	Lines  []string // each line with its ' ', '+' or '-' prefix
}

// ParseUnifiedDiff splits `git diff` output (without renames) into files and hunks.
//
// A hunk's id is the first 16 hex digits of the SHA-256 of its path and its lines, with a
// counter for identical hunks in one file. The header's line numbers are left out on purpose:
// an earlier hunk that grows moves every later one, and a reviewer's decision on a hunk must
// not be lost because a different hunk changed (REQ-WT-003, REQ-DSK-002).
func ParseUnifiedDiff(text string) []FileDiff {
	var files []FileDiff
	var cur *FileDiff
	var hunk *Hunk
	seen := map[string]int{}
	flush := func() {
		if hunk != nil && cur != nil {
			key := cur.Path + "\x00" + strings.Join(hunk.Lines, "\n")
			n := seen[key]
			seen[key] = n + 1
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", key, n)))
			hunk.ID = hex.EncodeToString(sum[:])[:16]
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			files = append(files, FileDiff{Path: pathFromDiffLine(line), Status: "modified"})
			cur = &files[len(files)-1]
			seen = map[string]int{}
		case cur == nil:
		case hunk == nil && strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case hunk == nil && strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case hunk == nil && strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
		case strings.HasPrefix(line, "@@ "):
			flush()
			header := line
			if end := strings.Index(line[3:], " @@"); end >= 0 {
				header = line[:3+end+3]
			}
			hunk = &Hunk{Header: header}
		case hunk != nil && line != "" && strings.ContainsRune(" +-\\", rune(line[0])):
			hunk.Lines = append(hunk.Lines, line)
		}
	}
	flush()
	return files
}

// pathFromDiffLine reads the b-side path of "diff --git a/<p> b/<p>". Paths with spaces are
// written the same on both sides without renames, so the line splits at its midpoint. A path git
// quotes — one with a quote, a backslash or a control character — is written C-style on both
// sides, "a/<p>" "b/<p>", and unquoted here.
func pathFromDiffLine(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if half := (len(rest) - 1) / 2; strings.HasPrefix(rest, `"`) && half > 0 && rest[half] == ' ' {
		if p, err := strconv.Unquote(rest[half+1:]); err == nil && strings.HasPrefix(p, "b/") {
			return p[2:]
		}
	}
	if len(rest) > 4 && strings.HasPrefix(rest, "a/") {
		half := (len(rest) - 1) / 2
		if rest[half] == ' ' && strings.HasPrefix(rest[half+1:], "b/") {
			return rest[half+3:]
		}
	}
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

// DiffResult is a worktree's changes and the revision they were read from.
type DiffResult struct {
	Files []FileDiff
	// Revision names the content shown. A merge that passes it lands exactly that content or
	// nothing (MergeRequest.Revision).
	Revision string
}

// MergeRequest is `worktree.merge`.
type MergeRequest struct {
	ID      string
	Message string // the generated one when empty
	// Revision, when set, is the DiffResult.Revision the user reviewed: the merge is refused
	// `changed` if the worktree no longer holds it, so the agent cannot slip in work after the
	// review.
	Revision string
}

// MergeResult is what a merge made.
type MergeResult struct {
	Commit string // "" when the worktree changed nothing
	// Leftover is why the worktree's directory or branch is still there after the commit
	// landed, nil when both are gone. The merge stands either way.
	Leftover error
}

// CloseGroupRequest is `worktree.close_group` once the group is resolved to its threads.
type CloseGroupRequest struct {
	ThreadIDs []string
	// Discard names the worktrees to discard; each must belong to one of ThreadIDs.
	Discard []string
	Confirm bool
	// Guard runs each discard for its thread, and may refuse it — the agent runtime refuses
	// while the thread's turn runs. Nil runs the discard as it is.
	Guard func(ctx context.Context, threadID string, discard func() error) error
}

// GroupEntry is one worktree of a closed group.
type GroupEntry struct {
	Worktree Worktree
	// Changes is ChangesChanged or ChangesClean for a worktree that was active when the group
	// was read, and "" otherwise.
	Changes   string
	Discarded bool
}
