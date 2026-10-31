package worktrees

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ecrespo/umbral/internal/hunks"
	"github.com/ecrespo/umbral/internal/worktrees/domain"
)

// The review of a worktree before it lands: single hunks rejected, the files listed, read and
// edited by the user (delta `2026-10-hunks-file-tree`, decisions 2, 6, 7 and 8).

// RejectHunks reverts the named hunks of the diff read as req.Revision in the worktree's working
// copy and returns the diff after it. The hunks left are what a merge with the new revision lands.
func (s *Service) RejectHunks(ctx context.Context, req domain.RejectRequest) (domain.DiffResult, error) {
	if len(req.HunkIDs) == 0 {
		return domain.DiffResult{}, &domain.Refusal{Reason: domain.ReasonNoHunks, Detail: "name at least one hunk to reject"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.active(ctx, req.ID)
	if err != nil {
		return domain.DiffResult{}, err
	}
	text, revision, err := s.o.Git.Diff(ctx, w.RepoRoot, w.Path, w.BaseCommit)
	if err != nil {
		return domain.DiffResult{}, err
	}
	if revision != req.Revision {
		return domain.DiffResult{}, &domain.Refusal{Reason: domain.ReasonChanged, Detail: "the worktree changed since its diff was read"}
	}
	known := map[string]bool{}
	for _, f := range hunks.Parse(text) {
		for _, h := range f.Hunks {
			known[h.ID] = true
		}
	}
	for _, id := range req.HunkIDs {
		if !known[id] {
			return domain.DiffResult{}, &domain.Refusal{Reason: domain.ReasonUnknownHunk, Detail: id}
		}
	}
	if err := s.o.Git.Revert(ctx, w.RepoRoot, w.Path, revision, hunks.Patch(text, hunks.Set(req.HunkIDs))); err != nil {
		return domain.DiffResult{}, err
	}
	text, revision, err = s.o.Git.Diff(ctx, w.RepoRoot, w.Path, w.BaseCommit)
	if err != nil {
		return domain.DiffResult{}, err
	}
	return domain.DiffResult{Files: domain.ParseUnifiedDiff(text), Revision: revision}, nil
}

// Files lists the worktree's content as a merge would read it, each path with its diff status;
// a deleted file is listed with its status.
func (s *Service) Files(ctx context.Context, id string) (domain.FileTree, error) {
	w, err := s.active(ctx, id)
	if err != nil {
		return domain.FileTree{}, err
	}
	text, revision, err := s.o.Git.Diff(ctx, w.RepoRoot, w.Path, w.BaseCommit)
	if err != nil {
		return domain.FileTree{}, err
	}
	paths, err := s.o.Git.Tree(ctx, w.RepoRoot, revision)
	if err != nil {
		return domain.FileTree{}, err
	}
	status := map[string]string{}
	for _, f := range hunks.Parse(text) {
		status[f.Path] = f.Status
		if f.Status == "deleted" {
			paths = append(paths, f.Path)
		}
	}
	slices.Sort(paths)
	tree := domain.FileTree{Revision: revision}
	for _, p := range paths {
		if len(tree.Items) == domain.MaxTreeItems {
			tree.Truncated = true
			break
		}
		tree.Items = append(tree.Items, domain.TreeItem{Path: p, Status: status[p]})
	}
	return tree, nil
}

// cleanPath refuses what is not a clean relative path inside the worktree, and a path through
// a `.git` at any depth and in any case, as git's own path check does: a nested repository's
// hooks, and `.GIT` on a file system that ignores case.
func cleanPath(p string) error {
	if p == "" || !filepath.IsLocal(p) || path.Clean(p) != p || strings.Contains(p, `\`) {
		return &domain.Refusal{Reason: domain.ReasonBadPath, Detail: p}
	}
	for part := range strings.SplitSeq(p, "/") {
		if strings.EqualFold(part, ".git") {
			return &domain.Refusal{Reason: domain.ReasonBadPath, Detail: p}
		}
	}
	return nil
}

// throughLink refuses a path whose directories include a link: os.Root would refuse one that
// leads out of the worktree with an error of its own, and one that stays inside names the file
// twice. It is a bad path either way.
func throughLink(root *os.Root, p string) error {
	for i := strings.IndexByte(p, '/'); i >= 0; i = nextSlash(p, i) {
		info, err := root.Lstat(p[:i])
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return &domain.Refusal{Reason: domain.ReasonBadPath, Detail: p}
		}
	}
	return nil
}

// nextSlash is the index of the slash after i in p, or -1.
func nextSlash(p string, i int) int {
	j := strings.IndexByte(p[i+1:], '/')
	if j < 0 {
		return -1
	}
	return i + 1 + j
}

// ReadFile reads a file of the worktree inside its directory, never following a link out of it.
// A file that is not a regular one, is over MaxFileBytes or is not UTF-8 text has no content.
func (s *Service) ReadFile(ctx context.Context, id, p string) (domain.FileContent, error) {
	if err := cleanPath(p); err != nil {
		return domain.FileContent{}, err
	}
	w, err := s.active(ctx, id)
	if err != nil {
		return domain.FileContent{}, err
	}
	root, err := os.OpenRoot(w.Path)
	if err != nil {
		return domain.FileContent{}, err
	}
	defer func() { _ = root.Close() }()
	return readIn(root, p)
}

func readIn(root *os.Root, p string) (domain.FileContent, error) {
	out := domain.FileContent{Path: p}
	if err := throughLink(root, p); err != nil {
		return out, err
	}
	info, err := root.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return out, fmt.Errorf("%w: %s", domain.ErrNotFound, p)
	}
	if err != nil {
		return out, err
	}
	out.Size = info.Size()
	if !info.Mode().IsRegular() {
		return out, nil
	}
	f, err := root.Open(p)
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	var head bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(h, &limited{w: &head, n: domain.MaxFileBytes + 1}), f); err != nil {
		return out, err
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	if head.Len() > domain.MaxFileBytes {
		return out, nil
	}
	if !isText(head.Bytes()) {
		out.Binary = true
		return out, nil
	}
	text := head.String()
	out.Content, out.Editable = &text, true
	return out, nil
}

// limited keeps the first n bytes written to it and discards the rest.
type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func isText(b []byte) bool { return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 }

// WriteFile replaces an existing regular text file's content with the user's, when its content
// is still the one read (ExpectedSHA256). It is written to a temporary file in the same directory
// and renamed over it, inside the worktree's directory, keeping the file's mode.
func (s *Service) WriteFile(ctx context.Context, req domain.WriteFileRequest) (domain.FileContent, error) {
	if err := cleanPath(req.Path); err != nil {
		return domain.FileContent{}, err
	}
	switch {
	case len(req.Content) > domain.MaxFileBytes:
		return domain.FileContent{}, &domain.Refusal{Reason: domain.ReasonTooLarge, Detail: fmt.Sprintf("at most %d bytes", domain.MaxFileBytes)}
	case !isText([]byte(req.Content)):
		return domain.FileContent{}, &domain.Refusal{Reason: domain.ReasonNotText, Detail: "the content is not UTF-8 text without NUL"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.active(ctx, req.ID)
	if err != nil {
		return domain.FileContent{}, err
	}
	root, err := os.OpenRoot(w.Path)
	if err != nil {
		return domain.FileContent{}, err
	}
	defer func() { _ = root.Close() }()
	cur, err := readIn(root, req.Path)
	if err != nil {
		return domain.FileContent{}, err
	}
	if !cur.Editable {
		return domain.FileContent{}, &domain.Refusal{Reason: domain.ReasonNotEditable, Detail: req.Path}
	}
	if cur.SHA256 != req.ExpectedSHA256 {
		return domain.FileContent{}, &domain.Refusal{Reason: domain.ReasonChanged, Detail: req.Path + " changed since it was read"}
	}
	info, err := root.Lstat(req.Path)
	if err != nil {
		return domain.FileContent{}, err
	}
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	tmp := path.Join(path.Dir(req.Path), ".umbral-edit-"+hex.EncodeToString(nonce[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return domain.FileContent{}, err
	}
	_, err = f.WriteString(req.Content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// The temporary file was created under the umask; the original's mode is put back.
		err = root.Chmod(tmp, info.Mode().Perm())
	}
	if err == nil {
		err = root.Rename(tmp, req.Path)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return domain.FileContent{}, err
	}
	return readIn(root, req.Path)
}
