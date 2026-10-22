package domain

import (
	"strings"
	"testing"
)

const twoHunks = `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,3 +1,3 @@
 package a
-var x = 1
+var x = 2
 // end
@@ -10,2 +10,3 @@ func f() {
 	return
+	// more
 }
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+hello
diff --git a/img.png b/img.png
new file mode 100644
index 0000000..4444444
Binary files /dev/null and b/img.png differ
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 5555555..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`

// TestParseUnifiedDiffSplitsFilesAndHunks_REQ_WT_003: a diff splits into files with their status
// and hunks, binary files have no hunks, and every hunk has an id.
func TestParseUnifiedDiffSplitsFilesAndHunks_REQ_WT_003(t *testing.T) {
	t.Parallel()
	files := ParseUnifiedDiff(twoHunks)
	if len(files) != 4 {
		t.Fatalf("%d files, want 4: %+v", len(files), files)
	}
	want := []struct {
		path, status string
		binary       bool
		hunks        int
	}{
		{"a.go", "modified", false, 2},
		{"new.txt", "added", false, 1},
		{"img.png", "added", true, 0},
		{"old.txt", "deleted", false, 1},
	}
	for i, w := range want {
		f := files[i]
		if f.Path != w.path || f.Status != w.status || f.Binary != w.binary || len(f.Hunks) != w.hunks {
			t.Errorf("file %d = %s %s binary=%v hunks=%d, want %+v", i, f.Path, f.Status, f.Binary, len(f.Hunks), w)
		}
	}
	h := files[0].Hunks[0]
	if h.Header != "@@ -1,3 +1,3 @@" || len(h.Lines) != 4 || h.Lines[1] != "-var x = 1" || len(h.ID) != 16 {
		t.Errorf("first hunk = %+v", h)
	}
}

// TestHunkIDsAreStable_REQ_WT_003: a hunk's id depends on its file and its lines, not on where
// it sits. The same diff yields the same ids, and a change in an earlier hunk that moves a later
// one leaves the later one's id alone; a changed hunk gets a new id.
func TestHunkIDsAreStable_REQ_WT_003(t *testing.T) {
	t.Parallel()
	first := ParseUnifiedDiff(twoHunks)
	again := ParseUnifiedDiff(twoHunks)
	if first[0].Hunks[1].ID != again[0].Hunks[1].ID || first[0].Hunks[0].ID != again[0].Hunks[0].ID {
		t.Fatal("the same diff gave different ids")
	}
	moved := strings.Replace(twoHunks, "@@ -10,2 +10,3 @@", "@@ -14,2 +14,3 @@", 1)
	moved = strings.Replace(moved, "+var x = 2", "+var x = 2\n+var y = 3", 1)
	m := ParseUnifiedDiff(moved)
	if m[0].Hunks[1].ID != first[0].Hunks[1].ID {
		t.Error("a hunk moved by an earlier change got a new id")
	}
	if m[0].Hunks[0].ID == first[0].Hunks[0].ID {
		t.Error("a changed hunk kept its id")
	}
	if first[0].Hunks[0].ID == first[0].Hunks[1].ID || first[1].Hunks[0].ID == first[3].Hunks[0].ID {
		t.Error("two different hunks share an id")
	}
}

// TestRepoIDIsSixteenHexOfTheCommonDir_REQ_WT_001: the repository id is the first 16 hex digits of
// the SHA-256 of the common git directory's real path.
func TestRepoIDIsSixteenHexOfTheCommonDir_REQ_WT_001(t *testing.T) {
	t.Parallel()
	// sha256("/repo/.git") = 4b8e…; the value is fixed so the layout on disk never drifts.
	got := RepoID("/repo/.git")
	if len(got) != 16 || strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("RepoID = %q, want 16 lowercase hex digits", got)
	}
	if RepoID("/repo/.git") != got || RepoID("/other/.git") == got {
		t.Error("RepoID is not a function of the path alone")
	}
	if BranchFor("thr_X") != "umbral/thr_X" {
		t.Errorf("BranchFor = %q", BranchFor("thr_X"))
	}
}

// TestGitVersionFloor_REQ_WT_008: 2.34 is the oldest git accepted; vendor suffixes are read.
func TestGitVersionFloor_REQ_WT_008(t *testing.T) {
	t.Parallel()
	for out, ok := range map[string]bool{
		"git version 2.34.0\n":                 true,
		"git version 2.43.0\n":                 true,
		"git version 2.39.5 (Apple Git-154)\n": true,
		"git version 3.0.0\n":                  true,
		"git version 2.33.8\n":                 false,
		"git version 1.9.5\n":                  false,
		"git version 2.45.1.windows.1\n":       true,
		"not git\n":                            false,
	} {
		if got := GitVersionSupported(out); got != ok {
			t.Errorf("GitVersionSupported(%q) = %v, want %v", out, got, ok)
		}
	}
}

// TestRefusalsAreValidationOrConflict_REQ_WT_004: the refusals that describe the request are
// validation errors, those that describe the repository's state are conflicts (API §8).
func TestRefusalsAreValidationOrConflict_REQ_WT_004(t *testing.T) {
	t.Parallel()
	for reason, conflict := range map[Reason]bool{
		ReasonNotGit: false, ReasonBare: false, ReasonOldGit: false, ReasonNoCommit: false, ReasonConfirmRequired: false, ReasonBadThread: false,
		ReasonDirty: true, ReasonOtherBranch: true, ReasonDetached: true, ReasonConflict: true,
		ReasonCommitFailed: true, ReasonNotActive: true, ReasonExists: true, ReasonChanged: true,
	} {
		r := &Refusal{Reason: reason}
		if r.Conflict() != conflict {
			t.Errorf("%s: Conflict() = %v, want %v", reason, r.Conflict(), conflict)
		}
		if !strings.Contains(r.Error(), string(reason)) {
			t.Errorf("%s: Error() = %q does not name the reason", reason, r.Error())
		}
	}
}

// TestThreadIDsThatCanNameAPath_REQ_WT_001: a thread id becomes a branch and a directory, so only
// "thr_" and letters and digits are accepted.
func TestThreadIDsThatCanNameAPath_REQ_WT_001(t *testing.T) {
	t.Parallel()
	for id, ok := range map[string]bool{
		"thr_01JABCDEF": true, "thr_a": true,
		"": false, "thr_": false, "wt_01": false, "thr_../x": false, "thr_a/b": false, "thr_a b": false,
	} {
		if ValidThreadID(id) != ok {
			t.Errorf("ValidThreadID(%q) = %v, want %v", id, !ok, ok)
		}
	}
}

// TestQuotedDiffPathsAreUnquoted_REQ_WT_003: git writes a path with a quote, a backslash or a
// control character C-style; the parser returns the path itself.
func TestQuotedDiffPathsAreUnquoted_REQ_WT_003(t *testing.T) {
	t.Parallel()
	files := ParseUnifiedDiff("diff --git \"a/we\\\"ird\\tb.txt\" \"b/we\\\"ird\\tb.txt\"\nnew file mode 100644\n@@ -0,0 +1 @@\n+x\n")
	if len(files) != 1 || files[0].Path != "we\"ird\tb.txt" {
		t.Fatalf("files = %+v", files)
	}
}
