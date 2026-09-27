package builtin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aymanbagabas/go-udiff"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// maxReadBytes bounds what read_file returns: a model's context is the scarce thing.
// maxFileBytes bounds what any file tool reads at all: a file past it is refused rather than
// read into the daemon's memory, which every session shares.
const (
	maxReadBytes = 256 << 10
	maxFileBytes = 16 << 20
)

// openRegular opens a file for reading without blocking on a FIFO, and refuses anything that
// is not a regular file — a device such as /dev/zero would never end.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // the path is the call's target, which the policy decided on
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}

// readBounded reads a regular file of at most maxFileBytes.
func readBounded(path string) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%s is larger than %d MiB; search it with grep instead", path, maxFileBytes>>20)
	}
	return data, nil
}

// readFile is read_file: a text file, or a window of its lines.
type readFile struct{}

type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (readFile) Spec() domain.Spec {
	return domain.Spec{
		Name:        "read_file",
		Description: "Read a text file. Lines are numbered from 1; offset and limit read a window of them.",
		Risk:        secdomain.RiskReadOnly,
		InputSchema: object([]string{fPath}, map[string]any{
			fPath:    str("File path, absolute or relative to the working directory."),
			"offset": integer("First line to read, from 1.", 1),
			"limit":  integer("How many lines to read.", 1),
		}),
	}
}

func (readFile) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in readInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return secdomain.Action{ThreadID: env.ThreadID, Tool: "read_file", Risk: secdomain.RiskReadOnly, Target: target(env, in.Path)}, nil
}

func (readFile) Summary(input json.RawMessage) string {
	var in readInput
	_ = json.Unmarshal(input, &in)
	return "read " + in.Path
}

func (readFile) Run(_ context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	var in readInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	data, err := readBounded(target(env, in.Path))
	if err != nil {
		return domain.Result{}, err
	}
	if binary(data) {
		return domain.Result{}, fmt.Errorf("%s is not a text file", in.Path)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first := max(in.Offset, 1)
	last := len(lines)
	if in.Limit > 0 {
		last = min(last, first-1+in.Limit)
	}
	var b strings.Builder
	truncated := false
	for i := first; i <= last; i++ {
		line := fmt.Sprintf("%6d\t%s", i, lines[i-1])
		if b.Len()+len(line) > maxReadBytes {
			truncated = true
			last = i - 1
			break
		}
		b.WriteString(line)
	}
	text := b.String()
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if truncated {
		text += fmt.Sprintf("[truncated at line %d of %d: read the rest with offset]\n", last, len(lines))
	}
	return domain.Result{Summary: fmt.Sprintf("read %s (%d lines)", in.Path, max(last-first+1, 0)), Text: text}, nil
}

// binary reports whether data looks like something other than text: a NUL byte early on.
func binary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// writeFile is write_file: create or replace a file.
type writeFile struct{}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (writeFile) Spec() domain.Spec {
	return domain.Spec{
		Name:        "write_file",
		Description: "Create a file, or replace its whole content. Parent directories are created.",
		Risk:        secdomain.RiskWriteFS,
		InputSchema: object([]string{fPath, "content"}, map[string]any{
			fPath:     str("File path, absolute or relative to the working directory."),
			"content": str("The complete new content."),
		}),
	}
}

func (writeFile) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in writeInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return writeAction(env, "write_file", in.Path), nil
}

func writeAction(env domain.Env, tool, path string) secdomain.Action {
	return secdomain.Action{
		ThreadID: env.ThreadID, Tool: tool, Risk: secdomain.RiskWriteFS,
		Target: target(env, path), Cwd: realPath(env.Cwd), WriteRoot: realPath(env.WriteRoot),
	}
}

func (writeFile) Summary(input json.RawMessage) string {
	var in writeInput
	_ = json.Unmarshal(input, &in)
	return "write " + in.Path
}

func (writeFile) Preview(_ context.Context, env domain.Env, input json.RawMessage) (string, error) {
	var in writeInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	old, err := current(target(env, in.Path))
	if err != nil {
		return "", err
	}
	return diff(in.Path, old, in.Content), nil
}

func (w writeFile) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	d, err := w.Preview(ctx, env, input)
	if err != nil {
		return domain.Result{}, err
	}
	var in writeInput
	_ = json.Unmarshal(input, &in)
	if err := replace(writeTarget(env, in.Path), []byte(in.Content)); err != nil {
		return domain.Result{}, err
	}
	return domain.Result{Summary: fmt.Sprintf("wrote %s (%d bytes)", in.Path, len(in.Content)), Text: d}, nil
}

// editFile is edit_file: replace an exact piece of a file.
type editFile struct{}

type editInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (editFile) Spec() domain.Spec {
	return domain.Spec{
		Name: "edit_file",
		Description: "Replace old_string with new_string in a file. old_string must appear exactly once " +
			"unless replace_all is set; include enough surrounding lines to make it unique.",
		Risk: secdomain.RiskWriteFS,
		InputSchema: object([]string{fPath, "old_string", "new_string"}, map[string]any{
			fPath:         str("File path, absolute or relative to the working directory."),
			"old_string":  nonEmpty("The exact text to replace."),
			"new_string":  str("The text to put in its place."),
			"replace_all": map[string]any{kType: "boolean", kDescription: "Replace every occurrence."},
		}),
	}
}

func (editFile) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in editInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return writeAction(env, "edit_file", in.Path), nil
}

func (editFile) Summary(input json.RawMessage) string {
	var in editInput
	_ = json.Unmarshal(input, &in)
	return "edit " + in.Path
}

// edited is the file's content after the edit, or why the edit cannot be made.
func (editFile) edited(env domain.Env, in editInput) (old, updated string, err error) {
	data, err := readBounded(target(env, in.Path))
	if err != nil {
		return "", "", err
	}
	old = string(data)
	switch n := strings.Count(old, in.OldString); {
	case n == 0:
		return "", "", fmt.Errorf("old_string was not found in %s", in.Path)
	case n > 1 && !in.ReplaceAll:
		return "", "", fmt.Errorf("old_string appears %d times in %s: add context to make it unique, or set replace_all", n, in.Path)
	}
	if in.ReplaceAll {
		return old, strings.ReplaceAll(old, in.OldString, in.NewString), nil
	}
	return old, strings.Replace(old, in.OldString, in.NewString, 1), nil
}

func (e editFile) Preview(_ context.Context, env domain.Env, input json.RawMessage) (string, error) {
	var in editInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	old, updated, err := e.edited(env, in)
	if err != nil {
		return "", err
	}
	return diff(in.Path, old, updated), nil
}

func (e editFile) Run(_ context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	var in editInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	old, updated, err := e.edited(env, in)
	if err != nil {
		return domain.Result{}, err
	}
	if err := replace(writeTarget(env, in.Path), []byte(updated)); err != nil {
		return domain.Result{}, err
	}
	return domain.Result{Summary: "edited " + in.Path, Text: diff(in.Path, old, updated)}, nil
}

// writeTarget is where a write goes: the target the registry checked the grant against, or,
// for a direct call with none, the path resolved now.
func writeTarget(env domain.Env, path string) string {
	if env.Target != "" {
		return env.Target
	}
	return target(env, path)
}

// current is a file's content, or "" when it does not exist yet.
func current(path string) (string, error) {
	data, err := readBounded(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// diff is the unified diff of a change, labelled like git's.
func diff(path, old, updated string) string {
	return udiff.Unified("a/"+filepath.ToSlash(path), "b/"+filepath.ToSlash(path), old, updated)
}

// replace writes data to target, a path whose symlinks were resolved when the policy decided
// on it. Everything below its nearest existing directory happens inside an os.Root opened
// there, so a component swapped for a link since the decision cannot lead the write out of
// it: missing directories are created, the data goes to a temporary file that is renamed over
// the target, and an existing file keeps its mode. A target that has become a link, or is not
// a regular file, is refused.
func replace(target string, data []byte) error {
	anchor := filepath.Dir(target)
	for {
		if info, err := os.Lstat(anchor); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(anchor)
		if parent == anchor {
			return fmt.Errorf("no directory of %s exists", target)
		}
		anchor = parent
	}
	root, err := os.OpenRoot(anchor)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rel, err := filepath.Rel(anchor, target)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	mode := fs.FileMode(0o644)
	if info, err := root.Lstat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is no longer a regular file; it was not written", target)
		}
		mode = info.Mode().Perm()
	}
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".umbral-"+rand.Text()[:12])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}
