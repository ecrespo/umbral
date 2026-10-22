// Package gitcli drives the `git` command line for the worktree engine (constitution stack
// table: worktrees through the git CLI, 2.34 or later).
//
// Two kinds of command run here, and they are configured differently on purpose:
//   - Reading a worktree to show it — status, the diff — runs with everything a repository's
//     configuration can make git execute turned off: fsmonitor, filter drivers, external diff
//     drivers and textconv. The agent writes inside the worktree, and showing its work is no
//     decision the policy engine took (REQ-CTX-003's rule).
//   - Landing the work — the squash's snapshot, cherry-pick and commit — runs as the user's own
//     `git commit` would: their filters, hooks and signing (REQ-WT-004). It has no terminal:
//     standard input is closed, prompts are off, and the commit runs in a session of its own,
//     killed whole when its timeout passes, so a pinentry waiting for a passphrase cannot hold
//     the daemon.
package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ecrespo/umbral/internal/worktrees/domain"
	"github.com/ecrespo/umbral/internal/worktrees/ports"
)

// The git words the adapter uses most.
const (
	revParse  = "rev-parse"
	verify    = "--verify"
	headRef   = "HEAD"
	diffCmd   = "diff"
	nameOnly  = "--name-only"
	noRenames = "--no-renames"
)

// defaultTimeout bounds every git command but the merge's commit, which has its own.
const defaultTimeout = 60 * time.Second

// Options configures the CLI.
type Options struct {
	Bin     string        // the git binary; "git" when empty
	Env     []string      // added to the daemon's environment, later entries winning
	Timeout time.Duration // per command; defaultTimeout when zero
}

// CLI implements ports.Git over the git binary.
type CLI struct {
	bin     string
	env     []string
	timeout time.Duration
}

var _ ports.Git = (*CLI)(nil)

// New returns a CLI.
func New(o Options) *CLI {
	c := &CLI{bin: o.Bin, env: o.Env, timeout: o.Timeout}
	if c.bin == "" {
		c.bin = "git"
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	return c
}

// call is one git invocation.
type call struct {
	dir       string
	args      []string
	sanitized bool          // read-only display: nothing the repository's configuration names runs
	env       []string      // added after the CLI's own
	timeout   time.Duration // the CLI's when zero
	stdin     []byte        // standard input; /dev/null when nil
}

// result is a finished git invocation. err is non-nil when git exited non-zero or did not run.
type result struct {
	stdout, stderr string
	err            error
}

func (r result) exitCode() int {
	var exit *exec.ExitError
	if errors.As(r.err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func (r result) failure(what string) error {
	return fmt.Errorf("git %s: %w: %s", what, r.err, strings.TrimSpace(r.stderr))
}

// inheritedGitVars would point every command at another repository, index or object store than
// the one named, or carry configuration past the overrides the read path sets.
var inheritedGitVars = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_CONFIG_PARAMETERS=", "GIT_CONFIG_COUNT=",
	"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
}

func (c *CLI) environ(extra []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		drop := false
		for _, p := range inheritedGitVars {
			if strings.HasPrefix(kv, p) {
				drop = true
				break
			}
		}
		if !drop {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C", "GIT_EDITOR=false")
	env = append(env, c.env...)
	return append(env, extra...)
}

func (c *CLI) run(ctx context.Context, k call) result {
	timeout := k.timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-C", k.dir}
	if k.sanitized {
		overrides, err := c.filterOverrides(ctx, k.dir, k.env)
		if err != nil {
			return result{err: err}
		}
		args = append(args, "--no-optional-locks",
			"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
			"-c", "diff.external=", "-c", "color.ui=false", "-c", "core.quotePath=false")
		args = append(args, overrides...)
	}
	args = append(args, k.args...)
	cmd := exec.CommandContext(ctx, c.bin, args...) //nolint:gosec // fixed git subcommands over the daemon's own paths
	cmd.Env = c.environ(k.env)
	cmd.Stdin = nil // standard input is /dev/null: nothing may wait on a person
	if k.stdin != nil {
		cmd.Stdin = bytes.NewReader(k.stdin)
	}
	isolate(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("%w (after %s)", ctx.Err(), timeout)
	}
	return result{stdout: out.String(), stderr: errOut.String(), err: err}
}

// filterOverrides turns off every filter driver the configuration defines: status, diff and add
// run a driver's clean command on a file whose stat information changed. A name an override
// cannot carry stops the command rather than running the driver. The configuration is read with
// the command's own environment, so a pinned git directory is the one whose drivers are listed.
func (c *CLI) filterOverrides(ctx context.Context, dir string, env []string) ([]string, error) {
	r := c.run(ctx, call{dir: dir, env: env, args: []string{"config", "--null", nameOnly, "--get-regexp", `^filter\.`}})
	if r.err != nil {
		if r.exitCode() == 1 {
			return nil, nil // no filter driver configured
		}
		return nil, r.failure("config")
	}
	seen := map[string]bool{}
	var overrides []string
	for _, key := range strings.Split(r.stdout, "\x00") {
		first, last := strings.IndexByte(key, '.'), strings.LastIndexByte(key, '.')
		if first < 0 || last <= first {
			continue
		}
		name := key[first+1 : last]
		if seen[name] {
			continue
		}
		seen[name] = true
		if strings.ContainsAny(name, "=\n") {
			return nil, errors.New("a filter driver's name cannot be overridden, so git was not run")
		}
		for _, v := range []string{"clean=", "smudge=", "process=", "required=false"} {
			overrides = append(overrides, "-c", "filter."+name+"."+v)
		}
	}
	return overrides, nil
}

// Version implements ports.Git.
func (c *CLI) Version(ctx context.Context) (string, error) {
	r := c.run(ctx, call{dir: os.TempDir(), args: []string{"--version"}})
	if r.err != nil {
		return "", r.failure("--version")
	}
	return strings.TrimSpace(r.stdout), nil
}

// Inspect implements ports.Git.
func (c *CLI) Inspect(ctx context.Context, dir string) (ports.Repo, error) {
	r := c.run(ctx, call{dir: dir, args: []string{revParse, "--is-bare-repository"}})
	if r.err != nil {
		if strings.Contains(r.stderr, "not a git repository") || errors.Is(r.err, os.ErrNotExist) {
			return ports.Repo{}, &domain.Refusal{Reason: domain.ReasonNotGit, Detail: dir}
		}
		return ports.Repo{}, r.failure(revParse)
	}
	if strings.TrimSpace(r.stdout) == "true" {
		return ports.Repo{}, &domain.Refusal{Reason: domain.ReasonBare, Detail: dir}
	}
	r = c.run(ctx, call{dir: dir, args: []string{revParse, "--path-format=absolute", "--show-toplevel", "--git-common-dir"}})
	if r.err != nil {
		if strings.Contains(r.stderr, "must be run in a work tree") {
			return ports.Repo{}, &domain.Refusal{Reason: domain.ReasonNotGit, Detail: dir + " is inside a git directory"}
		}
		return ports.Repo{}, r.failure(revParse)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 2 {
		return ports.Repo{}, fmt.Errorf("git rev-parse: unexpected output %q", r.stdout)
	}
	var repo ports.Repo
	var err error
	if repo.Top, err = filepath.EvalSymlinks(lines[0]); err != nil {
		return ports.Repo{}, err
	}
	if repo.CommonDir, err = filepath.EvalSymlinks(lines[1]); err != nil {
		return ports.Repo{}, err
	}
	r = c.run(ctx, call{dir: dir, args: []string{revParse, verify, "-q", "HEAD^{commit}"}})
	if r.err != nil {
		return ports.Repo{}, &domain.Refusal{Reason: domain.ReasonNoCommit, Detail: repo.Top}
	}
	repo.Head = strings.TrimSpace(r.stdout)
	r = c.run(ctx, call{dir: dir, args: []string{"symbolic-ref", "-q", "--short", headRef}})
	switch {
	case r.err == nil:
		repo.Branch = strings.TrimSpace(r.stdout)
	case r.exitCode() != 1:
		return ports.Repo{}, r.failure("symbolic-ref")
	}
	return repo, nil
}

// ResolveCommit implements ports.Git.
func (c *CLI) ResolveCommit(ctx context.Context, dir, rev string) (string, error) {
	if strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("git: %q is not a revision", rev)
	}
	r := c.run(ctx, call{dir: dir, args: []string{revParse, verify, "-q", "--end-of-options", rev + "^{commit}"}})
	if r.err != nil {
		return "", fmt.Errorf("git: %q is not a commit in %s", rev, dir)
	}
	return strings.TrimSpace(r.stdout), nil
}

// AddWorktree implements ports.Git.
func (c *CLI) AddWorktree(ctx context.Context, repoRoot, path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if r := c.run(ctx, call{dir: repoRoot, args: []string{"worktree", "add", "-q", "-b", branch, "--", path, base}}); r.err != nil {
		return r.failure("worktree add")
	}
	return nil
}

// BranchExists implements ports.Git.
func (c *CLI) BranchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	r := c.run(ctx, call{dir: repoRoot, args: []string{"show-ref", verify, "-q", "refs/heads/" + branch}})
	switch {
	case r.err == nil:
		return true, nil
	case r.exitCode() == 1:
		return false, nil
	}
	return false, r.failure("show-ref")
}

// Dirty implements ports.Git: tracked changes only. Untracked files are the user's and are left
// alone; a squash that would overwrite one is refused as a conflict by git itself.
func (c *CLI) Dirty(ctx context.Context, dir string) (bool, error) {
	r := c.run(ctx, call{dir: dir, sanitized: true, args: []string{"status", "--porcelain", "--untracked-files=no", "--ignore-submodules=dirty"}})
	if r.err != nil {
		return false, r.failure("status")
	}
	return strings.TrimSpace(r.stdout) != "", nil
}

// pinned returns the environment that makes git read the worktree through the main repository's
// own record of it, never through the worktree's `.git` file. The agent writes inside the
// worktree, so that file could name a git directory of its choosing — and with it a
// configuration whose filter drivers, fsmonitor or hooks the snapshot would run outside the
// policy engine (Art. 5). The record, `<common dir>/worktrees/<name>/gitdir`, sits in the main
// repository's git directory, which the agent cannot write.
func (c *CLI) pinned(ctx context.Context, repoRoot, worktree string) (admin string, env []string, err error) {
	r := c.run(ctx, call{dir: repoRoot, args: []string{revParse, "--path-format=absolute", "--git-common-dir"}})
	if r.err != nil {
		return "", nil, r.failure(revParse)
	}
	common := strings.TrimSpace(r.stdout)
	want, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", nil, err
	}
	admins, err := filepath.Glob(filepath.Join(common, "worktrees", "*", "gitdir"))
	if err != nil {
		return "", nil, err
	}
	for _, record := range admins {
		b, err := os.ReadFile(record) //nolint:gosec // the main repository's own worktree records
		if err != nil {
			continue
		}
		got, err := filepath.EvalSymlinks(filepath.Dir(strings.TrimSpace(string(b))))
		if err != nil || got != want {
			continue
		}
		admin = filepath.Dir(record)
		return admin, []string{"GIT_DIR=" + admin, "GIT_WORK_TREE=" + want}, nil
	}
	return "", nil, fmt.Errorf("git: %s is not a worktree of %s", worktree, repoRoot)
}

// snapshot writes the worktree's whole working tree — committed, modified and untracked, minus
// what .gitignore excludes — as a tree object, through an index of its own so the worktree's
// index is never touched. The private index starts as a copy of the worktree's, so files whose
// stat information has not changed are not hashed again: with filters off, hashing an LFS file
// afresh would show its content where the repository holds a pointer.
func (c *CLI) snapshot(ctx context.Context, repoRoot, worktree string, sanitized bool) (string, error) {
	admin, env, err := c.pinned(ctx, repoRoot, worktree)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "umbral-index-*")
	if err != nil {
		return "", err
	}
	index := f.Name()
	defer func() { _ = os.Remove(index) }()
	steps := [][]string{{"add", "-A"}, {"write-tree"}}
	if own, err := os.Open(filepath.Join(admin, "index")); err == nil { //nolint:gosec // the main repository's record of the worktree
		_, err = io.Copy(f, own)
		_ = own.Close()
		if err != nil {
			_ = f.Close()
			return "", err
		}
	} else {
		_ = os.Remove(index) // git refuses an empty file as an index; it creates its own
		steps = append([][]string{{"read-tree", headRef}}, steps...)
	}
	_ = f.Close()
	env = append(env, "GIT_INDEX_FILE="+index)
	var r result
	for _, args := range steps {
		if r = c.run(ctx, call{dir: worktree, sanitized: sanitized, env: env, args: args}); r.err != nil {
			return "", r.failure(args[0])
		}
	}
	return strings.TrimSpace(r.stdout), nil
}

// Diff implements ports.Git.
func (c *CLI) Diff(ctx context.Context, repoRoot, worktree, base string) (text, revision string, err error) {
	tree, err := c.snapshot(ctx, repoRoot, worktree, true)
	if err != nil {
		return "", "", err
	}
	// Both sides are objects now, so the diff runs in the main copy and never reads the worktree.
	r := c.run(ctx, call{dir: repoRoot, sanitized: true, args: []string{
		diffCmd, "--no-ext-diff", "--no-textconv", "--no-color", noRenames, "--src-prefix=a/", "--dst-prefix=b/",
		"-U3", base, tree, "--",
	}})
	if r.err != nil {
		return "", "", r.failure(diffCmd)
	}
	return r.stdout, tree, nil
}

// Squash implements ports.Git. The main copy is clean when it is called (the engine checks), so
// `reset --hard HEAD` returns it exactly to where it was after a conflict or a failed commit.
func (c *CLI) Squash(ctx context.Context, repoRoot, worktree, base, revision, message string, timeout time.Duration) (string, error) {
	tree, err := c.snapshot(ctx, repoRoot, worktree, false)
	if err != nil {
		return "", err
	}
	// The reviewed revision is checked against the snapshot about to be committed, so nothing
	// written after the check can land. Where no filter driver changes a file, the two trees are
	// the same object. Where one does, every file that differs must be the user's clean filter
	// applied to the reviewed blob itself: the landed content is derived from what was reviewed,
	// never from a second reading of the worktree.
	if revision != "" && tree != revision {
		ok, err := c.filtered(ctx, repoRoot, worktree, revision, tree)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", &domain.Refusal{Reason: domain.ReasonChanged, Detail: "the worktree changed after the diff that was reviewed"}
		}
	}
	r := c.run(ctx, call{dir: repoRoot, args: []string{revParse, verify, base + "^{tree}"}})
	if r.err != nil {
		return "", r.failure(revParse)
	}
	if strings.TrimSpace(r.stdout) == tree {
		return "", nil // the worktree changed nothing
	}
	r = c.run(ctx, call{dir: repoRoot, args: []string{"commit-tree", "--no-gpg-sign", tree, "-p", base, "-m", "umbral worktree snapshot"}})
	if r.err != nil {
		return "", r.failure("commit-tree")
	}
	snap := strings.TrimSpace(r.stdout)
	head, err := c.head(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	// git's cherry-pick overwrites an untracked file the main copy ignores, and the rollback would
	// then delete it: a path the squash adds may not exist in the main copy at all.
	if paths, err := c.occupied(ctx, repoRoot, head, snap); err != nil {
		return "", err
	} else if len(paths) > 0 {
		return "", &domain.Refusal{Reason: domain.ReasonConflict, Paths: paths, Detail: "the main copy has untracked files at these paths"}
	}

	if r = c.run(ctx, call{dir: repoRoot, args: []string{"cherry-pick", "--no-commit", snap}}); r.err != nil {
		paths := c.unmerged(ctx, repoRoot)
		if len(paths) == 0 {
			paths = overwritten(r.stderr)
		}
		if rbErr := c.rollback(ctx, repoRoot); rbErr != nil {
			return "", errors.Join(r.failure("cherry-pick"), rbErr)
		}
		return "", &domain.Refusal{Reason: domain.ReasonConflict, Paths: paths, Output: domain.Clip(strings.TrimSpace(r.stderr))}
	}
	if r = c.run(ctx, call{dir: repoRoot, sanitized: true, args: []string{diffCmd, "--cached", "--quiet"}}); r.err == nil {
		// The branch already holds exactly this change: nothing to commit, nothing failed.
		return "", c.rollback(ctx, repoRoot)
	} else if r.exitCode() != 1 {
		return "", errors.Join(r.failure("diff --cached"), c.rollback(ctx, repoRoot))
	}
	r = c.run(ctx, call{dir: repoRoot, timeout: timeout, args: []string{"commit", "-q", "-m", message}})
	if r.err != nil {
		// A post-commit hook runs after HEAD has moved: if the timeout or a failure came then,
		// the work has landed and rolling back would lose the commit.
		if now, err := c.head(ctx, repoRoot); err == nil && now != head {
			return now, nil
		}
		if rbErr := c.rollback(ctx, repoRoot); rbErr != nil {
			return "", errors.Join(r.failure("commit"), rbErr)
		}
		out := strings.TrimSpace(r.stderr + "\n" + r.stdout)
		if ctx.Err() == nil && strings.Contains(r.err.Error(), context.DeadlineExceeded.Error()) {
			out = strings.TrimSpace(fmt.Sprintf("the commit did not finish within %s\n%s", timeout, out))
		}
		return "", &domain.Refusal{Reason: domain.ReasonCommitFailed, Output: domain.Clip(out)}
	}
	return c.head(ctx, repoRoot)
}

// filtered reports whether tree is revision with the clean filters applied: the same paths and
// modes, and every blob that differs is what `hash-object --path` makes of the reviewed blob.
//
// The attributes that choose a filter are the reviewed tree's own. hash-object runs in a
// directory of the daemon's that holds only the reviewed `.gitattributes` files above each path,
// so nothing the agent writes in its worktree — an ignored attributes file, a case variant of
// one, a change between two reads — has a say. The filter drivers themselves come from the main
// repository's configuration, through its record of the worktree, as the landing snapshot's did.
func (c *CLI) filtered(ctx context.Context, repoRoot, worktree, revision, tree string) (bool, error) {
	admin, _, err := c.pinned(ctx, repoRoot, worktree)
	if err != nil {
		return false, err
	}
	r := c.run(ctx, call{dir: repoRoot, sanitized: true, args: []string{"diff-tree", "-r", "-z", noRenames, revision, tree}})
	if r.err != nil {
		return false, r.failure("diff-tree")
	}
	// -z raw output: ":<mode> <mode> <blob> <blob> <status>" NUL "<path>" NUL, per file.
	fields := strings.Split(strings.TrimSuffix(r.stdout, "\x00"), "\x00")
	if len(fields)%2 != 0 {
		return false, fmt.Errorf("git diff-tree: unexpected output %q", r.stdout)
	}
	attrs, err := os.MkdirTemp("", "umbral-attrs-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(attrs) }()
	// git falls back to the index for an attributes file the working tree lacks, and the
	// worktree's index is the agent's to write: an index that does not exist leaves nothing to
	// fall back to. It is inside attrs, under `.git/`, which no path of a tree can name, so
	// placeAttributes never creates it and nobody else can.
	env := []string{"GIT_DIR=" + admin, "GIT_WORK_TREE=" + attrs, "GIT_INDEX_FILE=" + filepath.Join(attrs, ".git", "index")}
	placed := map[string]bool{}
	for i := 0; i < len(fields); i += 2 {
		meta, path := strings.Fields(strings.TrimPrefix(fields[i], ":")), fields[i+1]
		if len(meta) != 5 || meta[4] != "M" || meta[0] != meta[1] {
			return false, nil // a path added, removed or changed in kind: not a filter's doing
		}
		if err := c.placeAttributes(ctx, repoRoot, revision, attrs, path, placed); err != nil {
			return false, err
		}
		blob := c.run(ctx, call{dir: repoRoot, args: []string{"cat-file", "blob", meta[2]}})
		if blob.err != nil {
			return false, blob.failure("cat-file")
		}
		hashed := c.run(ctx, call{dir: attrs, env: env, stdin: []byte(blob.stdout), args: []string{"hash-object", "--path=" + path, "--stdin"}})
		if hashed.err != nil {
			return false, hashed.failure("hash-object")
		}
		if strings.TrimSpace(hashed.stdout) != meta[3] {
			return false, nil
		}
	}
	return true, nil
}

// placeAttributes writes, under dir, the reviewed tree's `.gitattributes` of every directory
// from the root down to path's own, as git would read them for path.
func (c *CLI) placeAttributes(ctx context.Context, repoRoot, revision, dir, path string, placed map[string]bool) error {
	parts := strings.Split(path, "/")
	for depth := range parts {
		rel := strings.Join(parts[:depth], "/")
		if placed[rel] {
			continue
		}
		placed[rel] = true
		name := ".gitattributes"
		if rel != "" {
			name = rel + "/" + name
		}
		r := c.run(ctx, call{dir: repoRoot, args: []string{"cat-file", "blob", revision + ":" + name}})
		if r.err != nil {
			continue // that directory has no attributes file in the reviewed tree
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(r.stdout), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (c *CLI) head(ctx context.Context, dir string) (string, error) {
	r := c.run(ctx, call{dir: dir, args: []string{revParse, verify, headRef}})
	if r.err != nil {
		return "", r.failure(revParse)
	}
	return strings.TrimSpace(r.stdout), nil
}

// occupied lists the paths the squash adds over HEAD that already exist in the main copy, as a
// file or as a file where the squash needs a directory. Both commits are objects, so this reads
// nothing from the worktree.
func (c *CLI) occupied(ctx context.Context, repoRoot, head, snap string) ([]string, error) {
	r := c.run(ctx, call{dir: repoRoot, sanitized: true, args: []string{
		diffCmd, nameOnly, "-z", noRenames, "--diff-filter=A", head, snap, "--",
	}})
	if r.err != nil {
		return nil, r.failure(diffCmd)
	}
	var paths []string
	for _, p := range strings.Split(r.stdout, "\x00") {
		if p == "" {
			continue
		}
		for prefix := p; prefix != "." && prefix != ""; prefix = filepath.Dir(prefix) {
			info, err := os.Lstat(filepath.Join(repoRoot, prefix))
			if err == nil && (prefix == p || !info.IsDir()) {
				paths = append(paths, p)
				break
			}
		}
	}
	return paths, nil
}

// unmerged lists the paths a failed cherry-pick left in conflict.
func (c *CLI) unmerged(ctx context.Context, dir string) []string {
	r := c.run(ctx, call{dir: dir, sanitized: true, args: []string{diffCmd, nameOnly, "-z", "--diff-filter=U"}})
	if r.err != nil {
		return nil
	}
	var paths []string
	for _, p := range strings.Split(r.stdout, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// overwritten reads the paths git lists, tab-indented, when it refuses to overwrite files.
func overwritten(stderr string) []string {
	var paths []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "\t") {
			paths = append(paths, strings.TrimSpace(line))
		}
	}
	return paths
}

// rollback returns a main copy that was clean to its HEAD, dropping what a cherry-pick staged and
// its CHERRY_PICK_HEAD. It runs on a fresh context so a cancelled request still cleans up.
func (c *CLI) rollback(ctx context.Context, dir string) error {
	ctx = context.WithoutCancel(ctx)
	if r := c.run(ctx, call{dir: dir, args: []string{"reset", "-q", "--hard", headRef}}); r.err != nil {
		return r.failure("reset")
	}
	return nil
}

// RemoveWorktree implements ports.Git.
func (c *CLI) RemoveWorktree(ctx context.Context, repoRoot, path, branch string) error {
	// The directory is removed here rather than by `git worktree remove`, which reads the
	// worktree's `.git` file — written by the agent, so not trusted (see pinned). Prune then
	// drops the main repository's record of a worktree whose directory is gone.
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if r := c.run(ctx, call{dir: repoRoot, args: []string{"worktree", "prune"}}); r.err != nil {
		return r.failure("worktree prune")
	}
	exists, err := c.BranchExists(ctx, repoRoot, branch)
	if err != nil || !exists {
		return err
	}
	if r := c.run(ctx, call{dir: repoRoot, args: []string{"branch", "-q", "-D", branch}}); r.err != nil {
		return r.failure("branch -D")
	}
	return nil
}
