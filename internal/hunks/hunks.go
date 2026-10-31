// Package hunks reads a unified diff as files and hunks, gives each hunk the id a review names it
// by, and applies a chosen subset: as a patch, or to a file's content (delta
// `2026-10-hunks-file-tree`, decision 1). It is a leaf: the standard library only, so the worktree
// engine, the tools and the API read one diff the same way.
package hunks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// File is one file of a diff.
type File struct {
	Path   string
	Status string // added, modified or deleted
	Binary bool   // a binary file has no hunks
	Hunks  []Hunk

	head []string // the lines before the first hunk, `diff --git` to `+++`
}

// Hunk is one hunk of a file's diff. Its ID is stable while the hunk does not change.
type Hunk struct {
	ID     string
	Header string   // the "@@ -a,b +c,d @@" line, without the section heading git appends
	Lines  []string // each line with its ' ', '+', '-' or '\' prefix

	raw                        string // the header line as written
	oldStart, oldLen, newStart int
	oldLeft, newLeft           int
}

// Set is a set of hunk ids.
func Set(ids []string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

const noNewline = `\`

// Parse splits a unified diff — `git diff` output without renames, or a single file's
// `--- a/<p>` / `+++ b/<p>` diff — into files and hunks.
//
// A hunk's id is the first 16 hex digits of the SHA-256 of its path and its lines, with a
// counter for identical hunks in one file. The header's line numbers are left out on purpose:
// an earlier hunk that grows moves every later one, and a reviewer's decision on a hunk must
// not be lost because a different hunk changed (REQ-WT-003, REQ-DSK-002). A hunk ends when the
// line counts of its header are used up, so a removed line that reads `--- x` stays in it.
func Parse(text string) []File {
	var files []File
	var cur *File
	var hunk *Hunk
	seen := map[string]int{}
	flush := func() {
		if hunk != nil && cur != nil {
			key := cur.Path + "\x00" + strings.Join(hunk.Lines, "\n")
			n := seen[key]
			seen[key] = n + 1
			sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d", key, n))
			hunk.ID = hex.EncodeToString(sum[:])[:16]
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	open := func() bool { return hunk != nil && (hunk.oldLeft > 0 || hunk.newLeft > 0) }
	start := func(path string) {
		flush()
		files = append(files, File{Path: path, Status: "modified"})
		cur = &files[len(files)-1]
		seen = map[string]int{}
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case open():
			if line == "" || !strings.ContainsRune(" +-\\", rune(line[0])) {
				continue
			}
			hunk.Lines = append(hunk.Lines, line)
			switch line[0] {
			case ' ':
				hunk.oldLeft--
				hunk.newLeft--
			case '-':
				hunk.oldLeft--
			case '+':
				hunk.newLeft--
			}
		case hunk != nil && strings.HasPrefix(line, noNewline):
			hunk.Lines = append(hunk.Lines, line)
		case strings.HasPrefix(line, "diff --git "):
			start(pathFromDiffLine(line))
			cur.head = append(cur.head, line)
		case strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") &&
			(cur == nil || hunk != nil || len(cur.Hunks) > 0):
			// A diff without `diff --git` lines starts each file here.
			start(strings.TrimPrefix(strings.TrimPrefix(lines[i+1], "+++ "), "b/"))
			cur.head = append(cur.head, line)
		case cur == nil:
		case strings.HasPrefix(line, "@@ "):
			flush()
			h, err := header(line)
			if err != nil {
				continue
			}
			hunk = &h
		case hunk == nil:
			cur.head = append(cur.head, line)
			switch {
			case strings.HasPrefix(line, "new file mode"):
				cur.Status = "added"
			case strings.HasPrefix(line, "deleted file mode"):
				cur.Status = "deleted"
			case strings.HasPrefix(line, "Binary files "):
				cur.Binary = true
			case strings.HasPrefix(line, "--- /dev/null"):
				cur.Status = "added"
			case strings.HasPrefix(line, "+++ /dev/null"):
				cur.Status = "deleted"
			}
		}
	}
	flush()
	return files
}

// header reads "@@ -a[,b] +c[,d] @@[ heading]".
func header(line string) (Hunk, error) {
	h := Hunk{raw: line, Header: line}
	end := strings.Index(line[3:], " @@")
	if end < 0 {
		return h, errors.New("hunks: a hunk header without its closing @@")
	}
	h.Header = line[:3+end+3]
	ranges := strings.Fields(line[3 : 3+end])
	if len(ranges) != 2 || !strings.HasPrefix(ranges[0], "-") || !strings.HasPrefix(ranges[1], "+") {
		return h, fmt.Errorf("hunks: a hunk header %q", line)
	}
	var err error
	if h.oldStart, h.oldLen, err = span(ranges[0][1:]); err != nil {
		return h, err
	}
	var newLen int
	if h.newStart, newLen, err = span(ranges[1][1:]); err != nil {
		return h, err
	}
	h.oldLeft, h.newLeft = h.oldLen, newLen
	return h, nil
}

// span reads "start[,len]"; a missing len is 1.
func span(s string) (start, n int, err error) {
	a, b, found := strings.Cut(s, ",")
	if start, err = strconv.Atoi(a); err != nil {
		return 0, 0, err
	}
	n = 1
	if found {
		if n, err = strconv.Atoi(b); err != nil {
			return 0, 0, err
		}
	}
	return start, n, nil
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

// Patch is text cut down to the hunks keep holds: each file with one of them keeps its header
// lines and those hunks, as written; a file with none is left out. "" when keep holds none.
func Patch(text string, keep map[string]bool) string {
	var b strings.Builder
	for _, f := range Parse(text) {
		var chosen []Hunk
		for _, h := range f.Hunks {
			if keep[h.ID] {
				chosen = append(chosen, h)
			}
		}
		if len(chosen) == 0 {
			continue
		}
		for _, l := range f.head {
			b.WriteString(l + "\n")
		}
		for _, h := range chosen {
			b.WriteString(h.raw + "\n")
			for _, l := range h.Lines {
				b.WriteString(l + "\n")
			}
		}
	}
	return b.String()
}

// Apply is old with only the hunks of f that keep holds applied. Every hunk's old side must
// match old where its header puts it, kept or not, or the diff is not old's and Apply refuses.
func Apply(old string, f File, keep map[string]bool) (string, error) {
	src := strings.SplitAfter(old, "\n")
	if src[len(src)-1] == "" {
		src = src[:len(src)-1]
	}
	var out strings.Builder
	pos := 0
	for _, h := range f.Hunks {
		at := h.oldStart - 1
		if h.oldLen == 0 {
			at = h.oldStart // an insertion's start is the line it follows
		}
		if at < pos || at > len(src) {
			return "", fmt.Errorf("hunks: hunk %s of %s does not fit the file", h.ID, f.Path)
		}
		for _, l := range src[pos:at] {
			out.WriteString(l)
		}
		oldSide, newSide := sides(h)
		if at+len(oldSide) > len(src) || strings.Join(src[at:at+len(oldSide)], "") != strings.Join(oldSide, "") {
			return "", fmt.Errorf("hunks: hunk %s of %s does not match the file", h.ID, f.Path)
		}
		side := oldSide
		if keep[h.ID] {
			side = newSide
		}
		for _, l := range side {
			out.WriteString(l)
		}
		pos = at + len(oldSide)
	}
	for _, l := range src[pos:] {
		out.WriteString(l)
	}
	return out.String(), nil
}

// sides are a hunk's old and new lines, each with its line end; a line a `\ No newline` marker
// follows has none, on the side or sides it belongs to.
func sides(h Hunk) (oldSide, newSide []string) {
	trim := func(side []string) {
		if n := len(side); n > 0 {
			side[n-1] = strings.TrimSuffix(side[n-1], "\n")
		}
	}
	var prev byte
	for _, l := range h.Lines {
		switch l[0] {
		case ' ':
			oldSide = append(oldSide, l[1:]+"\n")
			newSide = append(newSide, l[1:]+"\n")
		case '-':
			oldSide = append(oldSide, l[1:]+"\n")
		case '+':
			newSide = append(newSide, l[1:]+"\n")
		case '\\':
			switch prev {
			case ' ':
				trim(oldSide)
				trim(newSide)
			case '-':
				trim(oldSide)
			case '+':
				trim(newSide)
			}
		}
		prev = l[0]
	}
	return oldSide, newSide
}
