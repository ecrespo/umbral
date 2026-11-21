package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ecrespo/umbral/internal/hunks"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/adapters/registry"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

func in(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestEditFileProducesDiff_REQ_AGT_012: edit_file and write_file show the unified diff of the
// change before it is made — what approval.requested carries — and the preview changes
// nothing; running the edit applies exactly that diff.
func TestEditFileProducesDiff_REQ_AGT_012(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := domain.Env{Cwd: dir, WriteRoot: dir}
	path := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	write(t, path, original)

	edit := in(map[string]any{"path": "main.go", "old_string": `println("hi")`, "new_string": `println("hello")`})
	d, err := editFile{}.Preview(context.Background(), env, edit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- a/main.go", "+++ b/main.go", "@@", "-\tprintln(\"hi\")", "+\tprintln(\"hello\")", " func main() {"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if read(t, path) != original {
		t.Fatal("the preview changed the file")
	}
	res, err := editFile{}.Run(context.Background(), env, edit)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != strings.Replace(original, `"hi"`, `"hello"`, 1) {
		t.Errorf("after the edit:\n%s", got)
	}
	if res.Text != d {
		t.Error("the result's diff is not the previewed one")
	}

	nd, err := writeFile{}.Preview(context.Background(), env, in(map[string]any{"path": "new/notes.md", "content": "one\ntwo\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nd, "+one\n+two\n") || !strings.Contains(nd, "+++ b/new/notes.md") {
		t.Errorf("a new file's diff:\n%s", nd)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); err == nil {
		t.Error("previewing a write created its directory")
	}
}

// TestEditFileNeedsAUniqueMatch_REQ_AGT_012: an old_string that is absent, or present more
// than once without replace_all, is an error that changes nothing; replace_all replaces all.
func TestEditFileNeedsAUniqueMatch_REQ_AGT_012(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := domain.Env{Cwd: dir}
	path := filepath.Join(dir, "f.txt")
	write(t, path, "a x a x a\n")

	for _, input := range []map[string]any{
		{"path": "f.txt", "old_string": "zzz", "new_string": "y"},
		{"path": "f.txt", "old_string": "a", "new_string": "b"},
	} {
		if _, err := (editFile{}).Run(context.Background(), env, in(input)); err == nil {
			t.Errorf("%v: no error", input)
		}
		if _, err := (editFile{}).Preview(context.Background(), env, in(input)); err == nil {
			t.Errorf("%v: preview gave no error", input)
		}
	}
	if read(t, path) != "a x a x a\n" {
		t.Fatal("a refused edit changed the file")
	}
	if _, err := (editFile{}).Run(context.Background(), env, in(map[string]any{"path": "f.txt", "old_string": "a", "new_string": "b", "replace_all": true})); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "b x b x b\n" {
		t.Errorf("replace_all gave %q", got)
	}
	if _, err := (editFile{}).Run(context.Background(), env, in(map[string]any{"path": "f.txt", "old_string": "x", "new_string": "y"})); err == nil {
		t.Error("an ambiguous edit ran after replace_all")
	}
}

// TestWriteFileCreatesAndKeepsTheMode: write_file creates missing directories, replaces a
// file whole, and keeps an existing file's permissions.
func TestWriteFileCreatesAndKeepsTheMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := domain.Env{Cwd: dir}
	if _, err := (writeFile{}).Run(context.Background(), env, in(map[string]any{"path": "a/b/c.txt", "content": "one"})); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a", "b", "c.txt")
	if read(t, path) != "one" {
		t.Fatal("not written")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := (writeFile{}).Run(context.Background(), env, in(map[string]any{"path": path, "content": "two"}))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	// Windows keeps no permission bits beyond read-only, so there only the content is checked.
	if read(t, path) != "two" || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Errorf("content %q mode %v, want two and 0700", read(t, path), info.Mode().Perm())
	}
	if !strings.Contains(res.Text, "-one") || !strings.Contains(res.Text, "+two") || !strings.Contains(res.Summary, "3 bytes") {
		t.Errorf("result = %+v", res)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a", "b"))
	if len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}

// TestReadFileReadsAWindow: read_file numbers lines from 1, reads a window with offset and
// limit, resolves a relative path against the working directory and refuses a binary file.
func TestReadFileReadsAWindow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := domain.Env{Cwd: dir}
	write(t, filepath.Join(dir, "sub", "l.txt"), "one\ntwo\nthree\nfour")
	res, err := (readFile{}).Run(context.Background(), env, in(map[string]any{"path": "sub/l.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "     1\tone\n     2\ttwo\n     3\tthree\n     4\tfour\n" || res.Tainted {
		t.Errorf("whole file = %q", res.Text)
	}
	res, err = (readFile{}).Run(context.Background(), env, in(map[string]any{"path": "sub/l.txt", "offset": 2, "limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "     2\ttwo\n     3\tthree\n" || res.Summary != "read sub/l.txt (2 lines)" {
		t.Errorf("window = %q, %q", res.Text, res.Summary)
	}
	write(t, filepath.Join(dir, "b.bin"), "ab\x00cd")
	if _, err := (readFile{}).Run(context.Background(), env, in(map[string]any{"path": "b.bin"})); err == nil {
		t.Error("a binary file was read")
	}
	big := strings.Repeat(strings.Repeat("x", 99)+"\n", 5000)
	write(t, filepath.Join(dir, "big.txt"), big)
	res, err = (readFile{}).Run(context.Background(), env, in(map[string]any{"path": "big.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > maxReadBytes+200 || !strings.Contains(res.Text, "[truncated at line") {
		t.Errorf("a large file returned %d bytes without saying it was cut", len(res.Text))
	}
	a, _ := readFile{}.Action(domain.Env{Cwd: abs("/r")}, in(map[string]any{"path": "x"}))
	if a.Target != filepath.Join(abs("/r"), "x") {
		t.Errorf("target = %q", a.Target)
	}
}

// TestTheTargetFollowsSymlinks_REQ_AGT_013: the write-root test is lexical (Tech §5.3), so the
// tool resolves symlinks first: a link inside the write root that points outside yields a
// target outside it, and the policy asks. A path that does not exist yet resolves through its
// nearest existing parent, and the write root and cwd are resolved the same way, so a root
// reached through a link (macOS's /tmp) still contains its own files.
func TestTheTargetFollowsSymlinks_REQ_AGT_013(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "repo")
	outside := filepath.Join(base, "elsewhere")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	env := domain.Env{Cwd: root, WriteRoot: root}

	a, err := writeFile{}.Action(env, in(map[string]any{"path": "link/new/file.txt", "content": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Target != filepath.Join(outside, "new", "file.txt") {
		t.Errorf("target = %q, want the link resolved to %s", a.Target, outside)
	}
	if secdomain.Decide(a, secdomain.ModeAutoEdit, nil, false).Verdict != secdomain.VerdictAsk {
		t.Error("a write through a link out of the root was not asked about in auto-edit")
	}

	viaAlias := domain.Env{Cwd: filepath.Join(base, "alias"), WriteRoot: filepath.Join(base, "alias")}
	a, err = editFile{}.Action(viaAlias, in(map[string]any{"path": "main.go", "old_string": "a", "new_string": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Target != filepath.Join(root, "main.go") || a.WriteRoot != root || a.Cwd != root {
		t.Errorf("action = %+v, want everything resolved to %s", a, root)
	}
	if d := secdomain.Decide(a, secdomain.ModeAutoEdit, nil, false); d.Verdict != secdomain.VerdictAllow {
		t.Errorf("a write inside a root reached through a link: %s (%s)", d.Verdict, d.Reason)
	}

	r, err := readFile{}.Action(env, in(map[string]any{"path": "link/x"}))
	if err != nil || r.Target != filepath.Join(outside, "x") {
		t.Errorf("read target = %q, %v", r.Target, err)
	}
	g, err := grepTool{}.Action(env, in(map[string]any{"pattern": "x", "path": "link"}))
	if err != nil || g.Target != outside {
		t.Errorf("grep target = %q, %v", g.Target, err)
	}
}

// TestAWriteStaysUnderItsDirectory_REQ_AGT_013: a write with no write root to be reached from
// — a target outside it, which a person approved — runs inside an os.Root opened on the
// target's nearest existing directory, so its last directory or the target itself, swapped
// for a link out of it after the decision, fails the write instead of being followed.
func TestAWriteStaysUnderItsDirectory_REQ_AGT_013(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	decided := filepath.Join(root, "sub", "f")
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := replace("", decided, []byte("x")); err == nil {
		t.Error("the write followed a link out of its directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Error("a file was written outside")
	}

	// The target itself swapped for a link since the decision: refused, not replaced.
	target := filepath.Join(root, "g")
	write(t, target, "mine")
	write(t, filepath.Join(outside, "g"), "theirs")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "g"), target); err != nil {
		t.Fatal(err)
	}
	if err := replace("", target, []byte("x")); err == nil {
		t.Error("a target swapped for a link was written")
	}
	if read(t, filepath.Join(outside, "g")) != "theirs" {
		t.Error("the file behind the swapped link changed")
	}
}

// TestAWriteThroughALinkWritesItsTarget_REQ_AGT_012: a path that is a link names the file it
// points to — that is what the policy decides on and what the diff shows — so the write goes
// there and the link stays a link.
func TestAWriteThroughALinkWritesItsTarget_REQ_AGT_012(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real.txt")
	write(t, real, "old\n")
	link := filepath.Join(base, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	env := domain.Env{Cwd: base, WriteRoot: base}
	input := in(map[string]any{"path": "link.txt", "content": "new\n"})
	a, _ := writeFile{}.Action(env, input)
	d, err := writeFile{}.Preview(context.Background(), env, input)
	if err != nil {
		t.Fatal(err)
	}
	if a.Target != real || !strings.Contains(d, "-old") || !strings.Contains(d, "+new") {
		t.Errorf("target %q, diff:\n%s", a.Target, d)
	}
	if _, err := (writeFile{}).Run(context.Background(), env, input); err != nil {
		t.Fatal(err)
	}
	if read(t, real) != "new\n" {
		t.Errorf("the link's target holds %q", read(t, real))
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
}

// TestReadsAreBounded_REQ_AGT_002: a file tool reads only regular files of at most 16 MiB, so
// a device, a FIFO or a huge file cannot exhaust the daemon's memory or hang the call.
func TestReadsAreBounded_REQ_AGT_002(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := domain.Env{Cwd: dir, WriteRoot: dir}
	fifo := filepath.Join(dir, "pipe")
	mkfifo(t, fifo)
	huge := filepath.Join(dir, "huge.log")
	write(t, huge, strings.Repeat("a line of text\n", maxFileBytes/15+1))

	if _, err := (readFile{}).Run(context.Background(), env, in(map[string]any{"path": huge, "limit": 1})); err == nil ||
		!strings.Contains(err.Error(), "larger than 16 MiB") {
		t.Errorf("a text file over 16 MiB: err = %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, p := range []string{fifo, "/dev/zero", huge, dir} {
			if _, err := (readFile{}).Run(context.Background(), env, in(map[string]any{"path": p})); err == nil {
				t.Errorf("read_file %s: no error", p)
			}
			if _, err := (editFile{}).Run(context.Background(), env, in(map[string]any{"path": p, "old_string": "a", "new_string": "b"})); err == nil {
				t.Errorf("edit_file %s: no error", p)
			}
		}
		for _, p := range []string{fifo, "/dev/zero"} {
			if _, err := (grepTool{}).Run(context.Background(), env, in(map[string]any{"pattern": "x", "path": p})); err == nil {
				t.Errorf("grep %s: no error", p)
			}
		}
		if _, err := (grepTool{}).Run(context.Background(), env, in(map[string]any{"pattern": "x"})); err != nil {
			t.Errorf("grep over a directory holding a FIFO: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a file tool hung on a FIFO or a device")
	}
}

func TestClipKeepsCharactersWhole(t *testing.T) {
	t.Parallel()

	line := strings.Repeat("a", maxLineChars-1) + "é" + "tail"
	got := clip(line)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > maxLineChars+len("…") {
		t.Errorf("clip = %q", got[len(got)-10:])
	}
}

// TestAWriteRunsWithTheAcceptedHunks_REQ_DSK_002: a write or an edit approved for some hunks of
// its diff writes the old content with only those applied and tells the model which the user
// rejected; its preview's hunks are the ones an approval names. A file that no longer holds an
// accepted hunk writes nothing, and a tool that cannot apply part of itself is not run with ids.
func TestAWriteRunsWithTheAcceptedHunks_REQ_DSK_002(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := domain.Env{ThreadID: "thr_1", Cwd: dir, WriteRoot: dir}
	path := filepath.Join(dir, "a.txt")
	twelve := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\ntwelve\n"
	write(t, path, twelve)
	reg, err := registry.New(writeFile{}, editFile{}, readFile{})
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(call domain.Call, ids []string) (domain.Result, error) {
		t.Helper()
		action, err := reg.Action(env, call)
		if err != nil {
			t.Fatal(err)
		}
		return reg.Invoke(context.Background(), env, call, domain.Grant{
			Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}, Hunks: ids,
		})
	}
	both := strings.Replace(strings.Replace(twelve, "one", "uno", 1), "twelve", "doce", 1)
	call := domain.Call{Tool: "write_file", Input: in(map[string]any{"path": "a.txt", "content": both})}
	preview, err := reg.Preview(context.Background(), env, call)
	if err != nil {
		t.Fatal(err)
	}
	files := hunks.Parse(preview)
	if len(files) != 1 || len(files[0].Hunks) != 2 {
		t.Fatalf("the preview's hunks %+v", files)
	}
	first, last := files[0].Hunks[0], files[0].Hunks[1]

	res, err := invoke(call, []string{first.ID})
	if err != nil {
		t.Fatalf("a write of one hunk: %v", err)
	}
	if got := read(t, path); got != strings.Replace(twelve, "one", "uno", 1) {
		t.Errorf("after accepting the first hunk:\n%s", got)
	}
	if !strings.Contains(res.Text, "rejected") || !strings.Contains(res.Text, last.Header) || !strings.Contains(res.Summary, "1 of 2") {
		t.Errorf("the model is not told what was rejected: %+v", res)
	}

	// The file holds the first hunk now: an id of the old diff that is no longer in the new one
	// writes nothing.
	before := read(t, path)
	if _, err := invoke(call, []string{first.ID}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Errorf("a stale hunk: %v", err)
	}
	if read(t, path) != before {
		t.Error("a stale hunk wrote the file")
	}

	edit := domain.Call{Tool: "edit_file", Input: in(map[string]any{"path": "a.txt", "old_string": "twelve", "new_string": "XII"})}
	ep, _ := reg.Preview(context.Background(), env, edit)
	if _, err := invoke(edit, []string{hunks.Parse(ep)[0].Hunks[0].ID}); err != nil || !strings.Contains(read(t, path), "XII") {
		t.Errorf("an edit of its one hunk: %v\n%s", err, read(t, path))
	}

	if _, err := invoke(domain.Call{Tool: "read_file", Input: in(map[string]any{"path": "a.txt"})}, []string{first.ID}); !errors.Is(err, domain.ErrNotAuthorized) {
		t.Errorf("a tool without hunks run with ids: %v", err)
	}
}

// swapped builds a write root holding a/b/f and a directory outside it holding b/f, decides
// on root/a/b/f, and then replaces root/a by a link to the outside directory: what a process
// confined to the write root can do between a decision and the daemon's own write or read.
func swapped(t *testing.T) (root, outside, decided string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside = filepath.Join(base, "repo"), filepath.Join(base, "outside")
	write(t, filepath.Join(root, "a", "b", "f"), "inside\n")
	write(t, filepath.Join(outside, "b", "f"), "secret\n")
	a, err := writeFile{}.Action(domain.Env{Cwd: root, WriteRoot: root}, in(map[string]any{"path": "a/b/f", "content": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	decided = a.Target
	if decided != filepath.Join(root, "a", "b", "f") {
		t.Fatalf("decided on %q", decided)
	}
	if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "was-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "a")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	return root, outside, decided
}

// TestAWriteStaysUnderItsWriteRoot_REQ_AGT_013: a target inside the write root is written
// from the write root, so a directory between the two that was swapped for a link after the
// decision does not carry the write — which may have run unasked — out of it (T-HRD-22).
func TestAWriteStaysUnderItsWriteRoot_REQ_AGT_013(t *testing.T) {
	t.Parallel()

	for name, run := range map[string]func(env domain.Env) error{
		"write_file": func(env domain.Env) error {
			_, err := writeFile{}.Run(context.Background(), env, in(map[string]any{"path": "a/b/f", "content": "x"}))
			return err
		},
		"edit_file": func(env domain.Env) error {
			_, err := editFile{}.Run(context.Background(), env, in(map[string]any{"path": "a/b/f", "old_string": "secret", "new_string": "x"}))
			return err
		},
		"a new file": func(env domain.Env) error {
			env.Target = filepath.Join(filepath.Dir(env.Target), "new")
			_, err := writeFile{}.Run(context.Background(), env, in(map[string]any{"path": "a/b/new", "content": "x"}))
			return err
		},
		"read_file": func(env domain.Env) error {
			res, err := readFile{}.Run(context.Background(), env, in(map[string]any{"path": "a/b/f"}))
			if err == nil {
				return nil
			}
			_ = res
			return err
		},
	} {
		for _, spelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/root through a link=%v", name, spelled), func(t *testing.T) {
				t.Parallel()
				root, outside, decided := swapped(t)
				if spelled {
					// The write root as a thread may hold it: through a link, while the decided
					// target has every link resolved. The policy resolves the root before its
					// test, and so must the write.
					alias := filepath.Join(filepath.Dir(root), "alias")
					if err := os.Symlink(root, alias); err != nil {
						t.Fatal(err)
					}
					root = alias
				}
				if err := run(domain.Env{Cwd: root, WriteRoot: root, Target: decided}); err == nil {
					t.Error("the write followed a link out of the write root")
				}
				if got := read(t, filepath.Join(outside, "b", "f")); got != "secret\n" {
					t.Errorf("the file outside the write root now holds %q", got)
				}
				if _, err := os.Lstat(filepath.Join(outside, "b", "new")); err == nil {
					t.Error("a file was created outside the write root")
				}
			})
		}
	}
}

// TestAHostedWriteAndReadStayUnderTheWriteRoot_REQ_ACP_004: the same for a hosted agent's
// `fs/write_text_file` and `fs/read_text_file`, which the daemon performs for it: a read
// inside the write root is not asked about, so a link swapped in after the decision would
// hand the agent a file from outside it (T-HRD-22).
func TestAHostedWriteAndReadStayUnderTheWriteRoot_REQ_ACP_004(t *testing.T) {
	t.Parallel()

	root, outside, decided := swapped(t)
	if err := WriteText(root, decided, "x"); err == nil {
		t.Error("the write followed a link out of the write root")
	}
	if got := read(t, filepath.Join(outside, "b", "f")); got != "secret\n" {
		t.Errorf("the file outside the write root now holds %q", got)
	}
	if text, err := ReadText(root, decided, 0, 0); err == nil {
		t.Errorf("the read followed a link out of the write root and returned %q", text)
	}
	// The same with the write root held through a link, as the policy resolves it.
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(alias, decided, "x"); err == nil {
		t.Error("the write followed a link out of a write root held through a link")
	}
	if text, err := ReadText(alias, decided, 0, 0); err == nil {
		t.Errorf("the read followed a link out of a write root held through a link and returned %q", text)
	}
	if got := read(t, filepath.Join(outside, "b", "f")); got != "secret\n" {
		t.Errorf("the file outside the write root now holds %q", got)
	}

	// A link that stays inside the write root is followed, as before the swap mattered.
	if err := os.Remove(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("was-a", filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(root, decided, "kept\n"); err != nil {
		t.Errorf("a write through a link that stays inside: %v", err)
	}
	if text, err := ReadText(root, decided, 0, 0); err != nil || text != "kept\n" {
		t.Errorf("a read through a link that stays inside = %q, %v", text, err)
	}

	// A target outside the write root was approved by a person: it is still written and read.
	if err := WriteText(root, filepath.Join(outside, "b", "f"), "approved\n"); err != nil {
		t.Errorf("an approved write outside the write root: %v", err)
	}
	if text, err := ReadText(root, filepath.Join(outside, "b", "f"), 0, 0); err != nil || text != "approved\n" {
		t.Errorf("an approved read outside the write root = %q, %v", text, err)
	}
}
