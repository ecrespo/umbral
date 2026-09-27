package local

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/context/domain"
)

// isolateGit keeps the developer's own git configuration out of the fixture repositories.
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
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

// fixtureRepo is a repository with rules files at three depths.
func fixtureRepo(t *testing.T) (root, cwd string) {
	t.Helper()
	isolateGit(t)
	root = t.TempDir()
	git(t, root, "init", "-q")
	cwd = filepath.Join(root, "svc", "api")
	write(t, filepath.Join(root, "AGENTS.md"), "root agents")
	write(t, filepath.Join(root, "CRUSH.md"), "root crush")
	write(t, filepath.Join(root, "svc", "CLAUDE.md"), "svc claude")
	write(t, filepath.Join(root, "svc", "CLAUDE.local.md"), "svc claude local")
	write(t, filepath.Join(cwd, "WARP.md"), "api warp")
	write(t, filepath.Join(cwd, "AGENTS.md"), "api agents")
	// Above the repository root: never read.
	write(t, filepath.Join(filepath.Dir(root), "AGENTS.md"), "above the root")
	return root, cwd
}

func TestRulesFilesPrecedence_REQ_CTX_001(t *testing.T) {
	_, cwd := fixtureRepo(t)
	rules, err := New(Config{}).Rules(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rules))
	for _, r := range rules {
		got = append(got, r.Content)
	}
	want := []string{"api agents", "api warp", "svc claude local", "svc claude", "root agents", "root crush"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rules in precedence order:\n got %v\nwant %v", got, want)
	}
}

func TestRulesStopAtTheRepositoryRoot_REQ_CTX_001(t *testing.T) {
	isolateGit(t)
	outer := t.TempDir()
	write(t, filepath.Join(outer, "AGENTS.md"), "outside the repo")
	root := filepath.Join(outer, "repo")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	write(t, filepath.Join(root, "AGENTS.md"), "inside")
	rules, err := New(Config{}).Rules(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Content != "inside" {
		t.Fatalf("a rules file above the repository root must not be read: %+v", rules)
	}
}

func TestARulesFileThatIsNotRegularIsSkipped_REQ_CTX_001(t *testing.T) {
	root, _ := fixtureRepo(t)
	if err := os.Mkdir(filepath.Join(root, "WARP.md"), 0o750); err != nil {
		t.Fatal(err)
	}
	rules, err := New(Config{}).Rules(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if strings.HasSuffix(r.Path, "WARP.md") {
			t.Fatalf("a directory named WARP.md is not a rules file: %+v", r)
		}
	}
}

type fakeBlocks map[string]domain.BlockText

func (f fakeBlocks) BlockText(_ context.Context, id string) (domain.BlockText, error) {
	b, ok := f[id]
	if !ok {
		return domain.BlockText{}, errors.New("no such block")
	}
	return b, nil
}

func TestAttachments_REQ_CTX_002(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "main.go"), "package main\n")
	write(t, filepath.Join(dir, "pkg", "a.go"), "package pkg\n")
	exit := 1
	l := New(Config{Blocks: fakeBlocks{"blk_01": {ID: "blk_01", Command: "go test ./...", ExitCode: &exit, Plain: "--- FAIL: TestParse"}}})
	ctx := context.Background()

	file, err := l.Attach(ctx, dir, domain.Ref{Kind: domain.KindFile, Ref: "main.go"})
	if err != nil || file.Content != "package main\n" || file.Bytes != 13 {
		t.Fatalf("file: %+v %v", file, err)
	}

	listing, err := l.Attach(ctx, dir, domain.Ref{Kind: domain.KindDir, Ref: "."})
	if err != nil || !strings.Contains(listing.Content, "main.go") || !strings.Contains(listing.Content, "pkg/") {
		t.Fatalf("dir: %+v %v", listing, err)
	}

	block, err := l.Attach(ctx, dir, domain.Ref{Kind: domain.KindBlock, Ref: "blk_01"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"go test ./...", "exit code: 1", "--- FAIL: TestParse"} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("block attachment lacks %q: %q", want, block.Content)
		}
	}

	for _, bad := range []domain.Ref{
		{Kind: domain.KindFile, Ref: "missing.go"},
		{Kind: domain.KindFile, Ref: "pkg"},
		{Kind: domain.KindDir, Ref: "main.go"},
		{Kind: domain.KindBlock, Ref: "blk_nope"},
		{Kind: "socket", Ref: "x"},
	} {
		if _, err := l.Attach(ctx, dir, bad); !errors.Is(err, domain.ErrUnknownAttachment) {
			t.Fatalf("%+v: want ErrUnknownAttachment, got %v", bad, err)
		}
	}
}

func TestAttachmentTruncated_REQ_CTX_005(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "big.log"), strings.Repeat("line of log\n", 30000)) // 360000 bytes
	a, err := New(Config{}).Attach(context.Background(), dir, domain.Ref{Kind: domain.KindFile, Ref: "big.log"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Content) != domain.MaxAttachmentBytes || a.Bytes != 360000 || a.TruncatedBytes != 360000-domain.MaxAttachmentBytes {
		t.Fatalf("kept %d, bytes %d, truncated %d", len(a.Content), a.Bytes, a.TruncatedBytes)
	}
	if !strings.Contains(a.Render(), "97856 bytes omitted") {
		t.Fatal("the context must state the omitted bytes")
	}
}

func TestAFileAttachmentNeverBlocksOnAFifo_REQ_CTX_002(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	if _, err := New(Config{}).Attach(context.Background(), dir, domain.Ref{Kind: domain.KindFile, Ref: "pipe"}); !errors.Is(err, domain.ErrUnknownAttachment) {
		t.Fatalf("a FIFO is not a file attachment: %v", err)
	}
}

func TestGitContext_REQ_CTX_003(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	write(t, filepath.Join(root, "a.go"), "package a\n")
	git(t, root, "add", "a.go")
	git(t, root, "commit", "-q", "-m", "init")
	git(t, root, "switch", "-q", "-c", "fix/parse")
	write(t, filepath.Join(root, "a.go"), "package a\n\nvar X = 1\n")
	write(t, filepath.Join(root, "new.txt"), "n\n")
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	g, err := New(Config{}).Git(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if g == nil {
		t.Fatal("a cwd inside a repository has git context")
	}
	if g.Branch != "fix/parse" {
		t.Fatalf("branch %q", g.Branch)
	}
	if !strings.Contains(g.Status, " M a.go") || !strings.Contains(g.Status, "?? new.txt") {
		t.Fatalf("status %q", g.Status)
	}
	if !strings.Contains(g.DiffStat, "a.go | 2 ++") {
		t.Fatalf("diff --stat %q", g.DiffStat)
	}

	outside, err := New(Config{}).Git(context.Background(), t.TempDir())
	if err != nil || outside != nil {
		t.Fatalf("outside a repository there is no git context: %+v %v", outside, err)
	}
}

func TestGitContextOnADetachedHead_REQ_CTX_003(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	write(t, filepath.Join(root, "a"), "a")
	git(t, root, "add", "a")
	git(t, root, "commit", "-q", "-m", "one")
	git(t, root, "checkout", "-q", "--detach")
	g, err := New(Config{}).Git(context.Background(), root)
	if err != nil || g == nil || !strings.HasPrefix(g.Branch, "(detached at ") {
		t.Fatalf("detached head: %+v %v", g, err)
	}
}

func TestGitContextRunsNoRepositoryCommand_REQ_CTX_003(t *testing.T) {
	// A repository's config can name commands git runs: fsmonitor on status, an external diff
	// driver on diff. Reading context must run none of them.
	isolateGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "hook.sh")
	write(t, hook, "#!/bin/sh\ntouch "+marker+"\n")
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-q", "-m", "one")
	write(t, filepath.Join(root, "a.txt"), "two\n")
	git(t, root, "config", "core.fsmonitor", hook)
	git(t, root, "config", "diff.external", hook)
	write(t, filepath.Join(root, ".gitattributes"), "*.txt diff=evil\n")
	git(t, root, "config", "diff.evil.textconv", hook)

	if _, err := New(Config{}).Git(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("reading git context ran a command the repository's config names")
	}
}

func TestABinaryRulesFileIsSkipped_REQ_CTX_001(t *testing.T) {
	root, _ := fixtureRepo(t)
	write(t, filepath.Join(root, "CLAUDE.md"), "not\x00text")
	rules, err := New(Config{}).Rules(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if strings.HasSuffix(r.Path, "CLAUDE.md") {
			t.Fatalf("a binary rules file must not reach the prompt: %+v", r)
		}
	}
}

func TestARulesFileLinkedOutOfTheRepositoryIsNotRead_REQ_CTX_001(t *testing.T) {
	root, cwd := fixtureRepo(t)
	secret := filepath.Join(t.TempDir(), "credentials")
	write(t, secret, "aws_secret_access_key = not-for-the-prompt")
	if err := os.Symlink(secret, filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	// A link that stays inside the repository is followed.
	write(t, filepath.Join(root, "docs", "rules.md"), "linked inside")
	if err := os.Symlink(filepath.Join("docs", "rules.md"), filepath.Join(root, "WARP.md")); err != nil {
		t.Fatal(err)
	}
	rules, err := New(Config{}).Rules(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	inside := false
	for _, r := range rules {
		if strings.Contains(r.Content, "not-for-the-prompt") {
			t.Fatal("a rules file linked out of the repository reached the prompt")
		}
		inside = inside || r.Content == "linked inside"
	}
	if !inside {
		t.Fatal("a rules file linked inside the repository is read")
	}
}

func TestGitContextRunsNoFilterDriver_REQ_CTX_003(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "clean.sh")
	write(t, hook, "#!/bin/sh\ntouch "+marker+"\ncat\n")
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "filter.evil.clean", hook)
	git(t, root, "config", "filter.evil.required", "true")
	write(t, filepath.Join(root, ".gitattributes"), "*.txt filter=evil\n")
	write(t, filepath.Join(root, "a.txt"), "one\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "one")
	// Same size and an mtime older than the index: status must hash the file to know, which
	// is when git runs the clean filter.
	write(t, filepath.Join(root, "a.txt"), "two\n")
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "a.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("the fixture's own add should have run the filter: %v", err)
	}

	g, err := New(Config{}).Git(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("reading git context ran a filter driver the repository's config names")
	}
	if g.Unavailable != "" || !strings.Contains(g.Status, "a.txt") {
		t.Fatalf("with the driver off, status still reads the change: %+v", g)
	}
}

func TestAFailingGitIsStatedNotDropped_REQ_CTX_003(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	// A git that refuses the repository: the section says why rather than vanish.
	refusing := filepath.Join(t.TempDir(), "git")
	write(t, refusing, "#!/bin/sh\necho 'fatal: detected dubious ownership in repository' >&2\nexit 128\n")
	if err := os.Chmod(refusing, 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{Git: refusing}).Git(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || !strings.Contains(g.Unavailable, "dubious ownership") {
		t.Fatalf("a git that fails is stated in the context: %+v", g)
	}
	// No git at all is no git context, not a failure to report.
	if none, err := New(Config{Git: filepath.Join(t.TempDir(), "no-such-git")}).Git(context.Background(), root); err != nil || none != nil {
		t.Fatalf("without git: %+v %v", none, err)
	}
	// A cancelled turn is an error, not a context line.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Config{}).Git(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestAFilterNameThatCannotBeOverriddenStopsGit_REQ_CTX_003(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "filter.a=b.clean", "true")
	g, err := New(Config{}).Git(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || !strings.Contains(g.Unavailable, "filter driver") {
		t.Fatalf("git is not run on the worktree, and the context says why: %+v", g)
	}
}
