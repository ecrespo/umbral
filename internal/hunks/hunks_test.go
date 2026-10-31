package hunks

import (
	"strings"
	"testing"
)

const (
	oldText = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n"
	newText = "uno\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ndoce\nthirteen"
)

// toolDiff is what go-udiff, the tools' differ, writes for the same change: no `diff --git` line
// and no section heading.
const toolDiff = "--- a/a.txt\n+++ b/a.txt\n@@ -1,4 +1,4 @@\n-one\n+uno\n two\n three\n four\n" +
	"@@ -9,4 +9,5 @@\n nine\n ten\n eleven\n-twelve\n+doce\n+thirteen\n\\ No newline at end of file\n"

// gitDiff is what `git diff` writes for oldText → newText in a.txt.
const gitDiff = "diff --git a/a.txt b/a.txt\nindex 1111111..2222222 100644\n--- a/a.txt\n+++ b/a.txt\n" +
	"@@ -1,4 +1,4 @@\n-one\n+uno\n two\n three\n four\n" +
	"@@ -9,4 +9,5 @@ eight\n nine\n ten\n eleven\n-twelve\n+doce\n+thirteen\n\\ No newline at end of file\n"

// TestHunkIDsAreTheSameFromEitherDiff_REQ_DSK_002: an approval's diff, written by go-udiff, and a
// worktree's, written by git, give the same hunk the same id, so one review reads both; an id
// depends on the path and the lines, not on where the hunk sits.
func TestHunkIDsAreTheSameFromEitherDiff_REQ_DSK_002(t *testing.T) {
	t.Parallel()
	fromGit := Parse(gitDiff)
	fromTool := Parse(toolDiff)
	if len(fromGit) != 1 || len(fromTool) != 1 {
		t.Fatalf("files: git %+v, tool %+v", fromGit, fromTool)
	}
	g, u := fromGit[0], fromTool[0]
	if g.Path != "a.txt" || u.Path != "a.txt" || g.Status != "modified" || u.Status != "modified" {
		t.Errorf("paths %q %q, statuses %q %q", g.Path, u.Path, g.Status, u.Status)
	}
	if len(g.Hunks) != 2 || len(u.Hunks) != 2 {
		t.Fatalf("hunks: git %+v, tool %+v", g.Hunks, u.Hunks)
	}
	for i := range g.Hunks {
		if g.Hunks[i].ID != u.Hunks[i].ID {
			t.Errorf("hunk %d: git id %s, tool id %s\ngit %q\ntool %q", i, g.Hunks[i].ID, u.Hunks[i].ID, g.Hunks[i].Lines, u.Hunks[i].Lines)
		}
	}
	if g.Hunks[1].Header != "@@ -9,4 +9,5 @@" {
		t.Errorf("header %q keeps git's section heading", g.Hunks[1].Header)
	}
	// A hunk's removed line that starts with "--" is a line of the hunk, not a file header.
	tricky := Parse("--- a/b.txt\n+++ b/b.txt\n@@ -1,2 +1 @@\n--- x\n keep\n")
	if len(tricky) != 1 || len(tricky[0].Hunks) != 1 || tricky[0].Hunks[0].Lines[0] != "--- x" {
		t.Errorf("a removed `-- x` line: %+v", tricky)
	}
}

// TestApplyingChosenHunks_REQ_DSK_002: a file's content with every hunk applied is the new
// content, with none the old one, and with some exactly those; a hunk that does not match the
// content is refused; a patch of chosen hunks keeps each file's header and only those hunks.
func TestApplyingChosenHunks_REQ_DSK_002(t *testing.T) {
	t.Parallel()
	f := Parse(toolDiff)[0]
	first, second := f.Hunks[0].ID, f.Hunks[1].ID
	for _, c := range []struct {
		keep []string
		want string
	}{
		{[]string{first, second}, newText},
		{nil, oldText},
		{[]string{first}, strings.Replace(oldText, "one", "uno", 1)},
		{[]string{second}, strings.Replace(newText, "uno", "one", 1)},
	} {
		got, err := Apply(oldText, f, Set(c.keep))
		if err != nil || got != c.want {
			t.Errorf("keep %v: %q, %v\nwant %q", c.keep, got, err, c.want)
		}
	}
	added := Parse("--- a/n.txt\n+++ b/n.txt\n@@ -0,0 +1,2 @@\n+a\n+b\n")[0]
	if got, err := Apply("", added, Set([]string{added.Hunks[0].ID})); err != nil || got != "a\nb\n" {
		t.Errorf("a new file: %q, %v", got, err)
	}
	if _, err := Apply(strings.Replace(oldText, "twelve", "XII", 1), f, Set([]string{second})); err == nil {
		t.Error("a hunk applied over content it does not match")
	}

	patch := Patch(gitDiff, Set([]string{second}))
	want := "diff --git a/a.txt b/a.txt\nindex 1111111..2222222 100644\n--- a/a.txt\n+++ b/a.txt\n" +
		"@@ -9,4 +9,5 @@ eight\n nine\n ten\n eleven\n-twelve\n+doce\n+thirteen\n\\ No newline at end of file\n"
	if patch != want {
		t.Errorf("patch:\n%s\nwant:\n%s", patch, want)
	}
	if got := Patch(gitDiff, Set(nil)); got != "" {
		t.Errorf("a patch of no hunk: %q", got)
	}
}
