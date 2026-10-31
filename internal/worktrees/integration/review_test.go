package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/worktrees/domain"
)

const twelve = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n"

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// hunkWith is the id of the first hunk of path whose lines hold line.
func hunkWith(t *testing.T, d domain.DiffResult, path, line string) string {
	t.Helper()
	for _, f := range d.Files {
		for _, h := range f.Hunks {
			if f.Path == path && strings.Contains(strings.Join(h.Lines, "\n"), line) {
				return h.ID
			}
		}
	}
	t.Fatalf("no hunk of %s holds %q in %+v", path, line, d.Files)
	return ""
}

// TestRejectedHunksLeaveTheWorktree_REQ_DSK_002: rejecting hunks of a diff reverts exactly those
// in the worktree — a modified line, an untracked file — and keeps the rest, which a merge with
// the new revision lands alone. The ids are a diff's: a worktree changed since, an id the diff
// does not hold and an empty list are refused, and each refusal leaves the worktree as it was.
func TestRejectedHunksLeaveTheWorktree_REQ_DSK_002(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(f.repo, "a.txt", twelve)
	f.git(f.repo, "commit", "-qam", "twelve lines")
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", strings.Replace(strings.Replace(twelve, "one", "uno", 1), "twelve", "doce", 1))
	f.write(w.Path, "n.txt", "brand new\n")
	d, err := f.svc.Diff(t.Context(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, last, added := hunkWith(t, d, "a.txt", "+uno"), hunkWith(t, d, "a.txt", "+doce"), hunkWith(t, d, "n.txt", "+brand new")

	before := f.state(w.Path)
	_, err = f.svc.RejectHunks(t.Context(), domain.RejectRequest{ID: w.ID, Revision: d.Revision, HunkIDs: []string{"0000000000000000"}})
	wantRefusal(t, err, domain.ReasonUnknownHunk)
	_, err = f.svc.RejectHunks(t.Context(), domain.RejectRequest{ID: w.ID, Revision: d.Revision})
	wantRefusal(t, err, domain.ReasonNoHunks)
	f.write(w.Path, "a.txt", strings.Replace(f.read(w.Path, "a.txt"), "six", "seis", 1))
	_, err = f.svc.RejectHunks(t.Context(), domain.RejectRequest{ID: w.ID, Revision: d.Revision, HunkIDs: []string{last}})
	wantRefusal(t, err, domain.ReasonChanged)
	f.write(w.Path, "a.txt", strings.Replace(f.read(w.Path, "a.txt"), "seis", "six", 1))
	if got := f.state(w.Path); got != before {
		t.Fatalf("a refused reject changed the worktree:\n%s\nwant\n%s", got, before)
	}

	after, err := f.svc.RejectHunks(t.Context(), domain.RejectRequest{ID: w.ID, Revision: d.Revision, HunkIDs: []string{last, added}})
	if err != nil {
		t.Fatalf("RejectHunks: %v", err)
	}
	if got := f.read(w.Path, "a.txt"); got != strings.Replace(twelve, "one", "uno", 1) {
		t.Errorf("a.txt after the reject:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(w.Path, "n.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the rejected new file is still there: %v", err)
	}
	if len(after.Files) != 1 || len(after.Files[0].Hunks) != 1 || after.Files[0].Hunks[0].ID != first || after.Revision == d.Revision {
		t.Errorf("the diff after the reject: %+v", after)
	}

	if _, err := f.svc.Merge(t.Context(), domain.MergeRequest{ID: w.ID, Revision: after.Revision}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := f.read(f.repo, "a.txt"); got != strings.Replace(twelve, "one", "uno", 1) {
		t.Errorf("the merge landed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "n.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the merge landed the rejected file")
	}
}

// TestTheWorktreesFileTree_REQ_DSK_003: the tree lists the worktree's content — untracked files
// in, ignored ones out, a deleted file with its status — with every changed file marked; a file
// reads as text with its digest and opens editable; an edit needs that digest, keeps the mode
// and lands; a binary file, a link and a path outside the worktree or inside `.git` are not
// edited.
func TestTheWorktreesFileTree_REQ_DSK_003(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(f.repo, "gone.txt", "bye\n")
	f.write(f.repo, ".gitignore", "*.log\n")
	f.write(f.repo, "run.sh", "echo hi\n")
	if err := os.Chmod(filepath.Join(f.repo, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-qm", "more")
	w := f.create("thr_a")
	f.write(w.Path, "a.txt", "one\nTWO\nthree\n")
	f.write(w.Path, "dir/new.txt", "new\n")
	f.write(w.Path, "debug.log", "noise\n")
	f.write(w.Path, "blob.bin", "a\x00b")
	if err := os.Remove(filepath.Join(w.Path, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	f.write(filepath.Dir(outside), "secret.txt", "secret\n")
	if err := os.Symlink(outside, filepath.Join(w.Path, "link.txt")); err != nil {
		t.Fatal(err)
	}

	tree, err := f.svc.Files(t.Context(), w.ID)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	got := map[string]string{}
	for _, it := range tree.Items {
		got[it.Path] = it.Status
	}
	want := map[string]string{
		".gitignore": "", "a.txt": "modified", "blob.bin": "added", "dir/new.txt": "added",
		"gone.txt": "deleted", "link.txt": "added", "run.sh": "",
	}
	if len(got) != len(want) || tree.Truncated || tree.Revision == "" {
		t.Errorf("tree %+v, want %v", tree, want)
	}
	for p, s := range want {
		if st, ok := got[p]; !ok || st != s {
			t.Errorf("%s: %q (listed %v), want %q", p, st, ok, s)
		}
	}

	a, err := f.svc.ReadFile(t.Context(), w.ID, "a.txt")
	if err != nil || a.Content == nil || *a.Content != "one\nTWO\nthree\n" || a.SHA256 != sum("one\nTWO\nthree\n") || !a.Editable || a.Binary {
		t.Fatalf("a.txt reads %+v, %v", a, err)
	}
	for _, c := range []struct{ path, why string }{{"blob.bin", "binary"}, {"link.txt", "a link"}} {
		r, err := f.svc.ReadFile(t.Context(), w.ID, c.path)
		if err != nil || r.Content != nil || r.Editable {
			t.Errorf("%s (%s) reads %+v, %v: want no content, not editable", c.path, c.why, r, err)
		}
	}
	// A directory link out of the worktree is a bad path, as is .git at any depth, in any case.
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(w.Path, "out")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../repo/a.txt", "/etc/passwd", ".git/config", "dir/../a.txt", "", "out/secret.txt", ".GIT/config", "dir/.git/hooks/pre-commit"} {
		if _, err := f.svc.WriteFile(t.Context(), domain.WriteFileRequest{ID: w.ID, Path: bad, Content: "x", ExpectedSHA256: "0"}); err == nil {
			t.Errorf("%q was written", bad)
		} else if bad != "" {
			wantRefusal(t, err, domain.ReasonBadPath)
		}
		if _, err := f.svc.ReadFile(t.Context(), w.ID, bad); err == nil {
			t.Errorf("%q was read", bad)
		} else {
			wantRefusal(t, err, domain.ReasonBadPath)
		}
	}
	if _, err := f.svc.ReadFile(t.Context(), w.ID, "nope.txt"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a missing file: %v", err)
	}

	edit := domain.WriteFileRequest{ID: w.ID, Path: "a.txt", Content: "edited\n", ExpectedSHA256: sum("stale")}
	_, err = f.svc.WriteFile(t.Context(), edit)
	wantRefusal(t, err, domain.ReasonChanged)
	edit.ExpectedSHA256 = a.SHA256
	res, err := f.svc.WriteFile(t.Context(), edit)
	if err != nil || res.SHA256 != sum("edited\n") || f.read(w.Path, "a.txt") != "edited\n" {
		t.Fatalf("an edit: %+v, %v, file %q", res, err, f.read(w.Path, "a.txt"))
	}
	run, _ := f.svc.ReadFile(t.Context(), w.ID, "run.sh")
	mode, err := os.Stat(filepath.Join(w.Path, "run.sh"))
	if err != nil || mode.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh is checked out %v, %v; want it executable", mode, err)
	}
	if _, err := f.svc.WriteFile(t.Context(), domain.WriteFileRequest{ID: w.ID, Path: "run.sh", Content: "echo bye\n", ExpectedSHA256: run.SHA256}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(w.Path, "run.sh")); err != nil || info.Mode().Perm() != mode.Mode().Perm() {
		t.Errorf("run.sh's mode after an edit: %v, %v", info.Mode(), err)
	}
	_, err = f.svc.WriteFile(t.Context(), domain.WriteFileRequest{ID: w.ID, Path: "link.txt", Content: "x\n", ExpectedSHA256: sum("secret\n")})
	wantRefusal(t, err, domain.ReasonNotEditable)
	if f.read(filepath.Dir(outside), "secret.txt") != "secret\n" {
		t.Error("an edit reached through a link")
	}
	_, err = f.svc.WriteFile(t.Context(), domain.WriteFileRequest{ID: w.ID, Path: "a.txt", Content: "a\x00b", ExpectedSHA256: res.SHA256})
	wantRefusal(t, err, domain.ReasonNotText)
	_, err = f.svc.WriteFile(t.Context(), domain.WriteFileRequest{ID: w.ID, Path: "new.txt", Content: "x\n", ExpectedSHA256: sum("")})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("an edit created a file: %v", err)
	}
}
