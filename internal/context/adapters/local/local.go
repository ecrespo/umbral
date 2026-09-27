// Package local reads a turn's context from the machine the daemon runs on: rules files,
// attachments and git state (REQ-CTX-001, 002, 003, 005).
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/context/domain"
	"github.com/ecrespo/umbral/internal/context/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// Defaults for Config.
const (
	defaultGitTimeout = 5 * time.Second
	// maxGitBytes bounds each section of the git context: a repository with thousands of
	// changed files would otherwise fill the window before the conversation starts.
	maxGitBytes = 32 << 10
)

// Config wires the adapter.
type Config struct {
	// Blocks resolves `@block` attachments; without it every block attachment is unknown.
	Blocks ports.Blocks
	// Git is the git binary, "git" when empty.
	Git string
	// GitTimeout bounds each git command.
	GitTimeout time.Duration
}

// Local implements ports.Gatherer.
type Local struct {
	blocks     ports.Blocks
	git        string
	gitTimeout time.Duration
}

var _ ports.Gatherer = (*Local)(nil)

// New builds the adapter.
func New(cfg Config) *Local {
	l := &Local{blocks: cfg.Blocks, git: cfg.Git, gitTimeout: cfg.GitTimeout}
	if l.git == "" {
		l.git = "git"
	}
	if l.gitTimeout <= 0 {
		l.gitTimeout = defaultGitTimeout
	}
	return l
}

// isRepo reports whether dir is a repository root: it holds a `.git` entry, a directory or
// the file a worktree or a submodule has.
func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// Rules reads the rules files from the repository root down to cwd — the same root the
// policy calls the write root (Tech §5.3) — highest precedence first. A candidate that does
// not exist, is not a regular file, holds a NUL byte or resolves outside the root is skipped.
func (l *Local) Rules(ctx context.Context, cwd string) ([]domain.RulesFile, error) {
	root := secdomain.WriteRoot(cwd, isRepo)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	var out []domain.RulesFile
	for _, path := range domain.RulesCandidates(root, cwd) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A rules file is read because of where it is, not because anyone named it: a link
		// that leads out of the repository — a clone can carry `AGENTS.md -> ~/.aws/credentials`
		// — is not followed.
		target, err := filepath.EvalSymlinks(path)
		if err != nil || !within(realRoot, target) {
			continue
		}
		head, total, err := readHead(target)
		if err != nil {
			continue
		}
		a := domain.NewAttachment(domain.KindFile, path, head, total)
		if a.Binary {
			continue
		}
		out = append(out, domain.RulesFile{Path: path, Content: a.Content, TruncatedBytes: a.TruncatedBytes})
	}
	return out, nil
}

// readHead opens a regular file without blocking on a FIFO and reads at most
// MaxAttachmentBytes of it, returning them and the file's size.
func readHead(path string) ([]byte, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // the user's own attachment or a rules file of the thread's cwd
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s is not a regular file", path)
	}
	head, err := io.ReadAll(io.LimitReader(f, domain.MaxAttachmentBytes))
	if err != nil {
		return nil, 0, err
	}
	return head, max(info.Size(), int64(len(head))), nil
}

// Attach reads one attachment.
func (l *Local) Attach(ctx context.Context, cwd string, ref domain.Ref) (domain.Attachment, error) {
	switch ref.Kind {
	case domain.KindFile:
		head, total, err := readHead(resolve(cwd, ref.Ref))
		if err != nil {
			return domain.Attachment{}, fmt.Errorf("%w: file %s: %w", domain.ErrUnknownAttachment, ref.Ref, err)
		}
		return domain.NewAttachment(ref.Kind, ref.Ref, head, total), nil
	case domain.KindDir:
		listing, err := listDir(resolve(cwd, ref.Ref))
		if err != nil {
			return domain.Attachment{}, fmt.Errorf("%w: directory %s: %w", domain.ErrUnknownAttachment, ref.Ref, err)
		}
		return domain.NewAttachment(ref.Kind, ref.Ref, listing, int64(len(listing))), nil
	case domain.KindBlock:
		if l.blocks == nil {
			return domain.Attachment{}, fmt.Errorf("%w: block %s: no block history", domain.ErrUnknownAttachment, ref.Ref)
		}
		b, err := l.blocks.BlockText(ctx, ref.Ref)
		if err != nil {
			return domain.Attachment{}, fmt.Errorf("%w: block %s: %w", domain.ErrUnknownAttachment, ref.Ref, err)
		}
		text := []byte(blockText(b))
		return domain.NewAttachment(ref.Kind, ref.Ref, text, int64(len(text))), nil
	default:
		return domain.Attachment{}, fmt.Errorf("%w: kind %q", domain.ErrUnknownAttachment, ref.Kind)
	}
}

// within reports whether path is root or lies below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolve(cwd, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// listDir lists a directory's entries, sorted, a directory's name ending in "/" and a link
// shown with its target.
func listDir(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir) // a file is refused here, "not a directory"
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b bytes.Buffer
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
			name += "/"
		case e.Type()&os.ModeSymlink != 0:
			if target, err := os.Readlink(filepath.Join(dir, name)); err == nil {
				name += " -> " + target
			}
		}
		b.WriteString(name)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

func blockText(b domain.BlockText) string {
	exit := "unknown (the block did not report how it ended)"
	if b.ExitCode != nil {
		exit = fmt.Sprint(*b.ExitCode)
	}
	return fmt.Sprintf("$ %s\nexit code: %s\n\n%s", b.Command, exit, b.Plain)
}

// Git reads the cwd's git state. Git runs with every command a repository's configuration can
// name turned off — fsmonitor, filter drivers, external diff drivers and textconv — since in
// `auto-edit` the agent may write `.git/config` itself, and reading context is no tool call
// the policy decides on. A git that fails for another reason than "not a repository" — a
// timeout, an ownership refusal — leaves the context saying so rather than silently dropping
// the section (Tech §5.3c).
func (l *Local) Git(ctx context.Context, cwd string) (*domain.GitContext, error) {
	top, err := l.run(ctx, cwd, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(err.Error(), "not a git repository") || errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, nil // not a repository, or no git to tell
		}
		return &domain.GitContext{Root: cwd, Unavailable: gitFailure(err)}, nil
	}
	root := strings.TrimSpace(top)
	g := &domain.GitContext{Root: root}
	overrides, err := l.filterOverrides(ctx, root)
	if err != nil {
		g.Unavailable = gitFailure(err)
		return g, ctx.Err()
	}
	if g.Branch, err = l.branch(ctx, root, overrides); err != nil {
		g.Unavailable = gitFailure(err)
		return g, ctx.Err()
	}
	status, err := l.run(ctx, root, overrides, "status", "--short", "--ignore-submodules=dirty")
	if err != nil {
		g.Unavailable = gitFailure(err)
		return g, ctx.Err()
	}
	diff, err := l.run(ctx, root, overrides, "diff", "--stat", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty")
	if err != nil {
		g.Unavailable = gitFailure(err)
		return g, ctx.Err()
	}
	g.Status, g.DiffStat = bound(status), bound(diff)
	return g, nil
}

// errUnsafeFilterName is a filter driver whose name a `-c` override cannot carry.
var errUnsafeFilterName = errors.New("a filter driver's name cannot be overridden, so git was not run on the worktree")

// filterOverrides turns off every filter driver the configuration defines: status and diff run
// a driver's clean command on a file whose stat information changed.
func (l *Local) filterOverrides(ctx context.Context, root string) ([]string, error) {
	out, err := l.run(ctx, root, nil, "config", "--null", "--name-only", "--get-regexp", `^filter\.`)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil // no filter driver configured
		}
		return nil, err
	}
	seen := map[string]bool{}
	var overrides []string
	for _, key := range strings.Split(out, "\x00") {
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
			return nil, errUnsafeFilterName
		}
		for _, v := range []string{"clean=", "smudge=", "process=", "required=false"} {
			overrides = append(overrides, "-c", "filter."+name+"."+v)
		}
	}
	return overrides, nil
}

// gitFailure is a git failure as the context states it: its first line.
func gitFailure(err error) string {
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return msg
}

func (l *Local) branch(ctx context.Context, root string, overrides []string) (string, error) {
	if name, err := l.run(ctx, root, overrides, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		return strings.TrimSpace(name), nil
	}
	sha, err := l.run(ctx, root, overrides, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return "(detached at " + strings.TrimSpace(sha) + ")", nil
}

// bound keeps a git section under maxGitBytes, saying how much was left out.
func bound(s string) string {
	if len(s) <= maxGitBytes {
		return s
	}
	cut := strings.LastIndexByte(s[:maxGitBytes], '\n') + 1
	return s[:cut] + fmt.Sprintf("[truncated: %d bytes omitted]\n", len(s)-cut)
}

// run runs one git command in dir, with extra `-c` overrides, and returns its standard output.
func (l *Local) run(ctx context.Context, dir string, overrides []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, l.gitTimeout)
	defer cancel()
	full := append([]string{
		"-C", dir,
		"--no-optional-locks",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "diff.external=",
		"-c", "color.ui=false",
		"-c", "core.quotePath=false",
	}, overrides...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, l.git, full...) //nolint:gosec // fixed git subcommands; dir is the thread's cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}
