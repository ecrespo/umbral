package builtin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/tools/domain"
)

func tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "main.go"), "package main\n// TODO: one\n")
	write(t, filepath.Join(dir, "pkg", "a.go"), "package pkg\n// todo: two\n")
	write(t, filepath.Join(dir, "pkg", "deep", "b_test.go"), "package deep\n// TODO: three\n")
	write(t, filepath.Join(dir, "README.md"), "TODO in prose\n")
	write(t, filepath.Join(dir, ".git", "HEAD"), "TODO in git\n")
	write(t, filepath.Join(dir, "blob.bin"), "TODO\x00binary")
	return dir
}

// TestGrepSearchesTextFiles: grep prints path:line:text relative to its root, skips .git and
// binary files, filters by glob, and folds case when asked.
func TestGrepSearchesTextFiles(t *testing.T) {
	t.Parallel()

	env := domain.Env{Cwd: tree(t)}
	run := func(input map[string]any) string {
		t.Helper()
		res, err := (grepTool{}).Run(context.Background(), env, in(input))
		if err != nil {
			t.Fatal(err)
		}
		return res.Text
	}
	if got := run(map[string]any{"pattern": "TODO"}); got != "README.md:1:TODO in prose\nmain.go:2:// TODO: one\npkg/deep/b_test.go:2:// TODO: three\n" {
		t.Errorf("grep TODO =\n%s", got)
	}
	if got := run(map[string]any{"pattern": "todo", "ignore_case": true, "glob": "**/*.go"}); strings.Count(got, "\n") != 3 || strings.Contains(got, "README") {
		t.Errorf("case-folded .go grep =\n%s", got)
	}
	if got := run(map[string]any{"pattern": "TODO", "path": "pkg/deep/b_test.go"}); got != "b_test.go:2:// TODO: three\n" {
		t.Errorf("grep on one file =\n%s", got)
	}
	if got := run(map[string]any{"pattern": "nothing-here"}); got != "no matches\n" {
		t.Errorf("no match = %q", got)
	}
	if _, err := (grepTool{}).Run(context.Background(), env, in(map[string]any{"pattern": "("})); err == nil {
		t.Error("an invalid pattern was accepted")
	}
}

// TestSearchesStopAtTheirLimit: a grep with more matches than its limit says it stopped.
func TestSearchesStopAtTheirLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var b strings.Builder
	for i := range maxMatches + 10 {
		fmt.Fprintf(&b, "hit %d\n", i)
	}
	write(t, filepath.Join(dir, "many.txt"), b.String())
	res, err := (grepTool{}).Run(context.Background(), domain.Env{Cwd: dir}, in(map[string]any{"pattern": "hit"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Text, "many.txt:") != maxMatches || !strings.Contains(res.Text, "[stopped at 500 matches") {
		t.Errorf("limit not applied or not reported: %s", res.Summary)
	}
}

// TestGlobAndListDir: glob matches relative paths with ** across directories, and list_dir
// marks directories.
func TestGlobAndListDir(t *testing.T) {
	t.Parallel()

	env := domain.Env{Cwd: tree(t)}
	res, err := (globTool{}).Run(context.Background(), env, in(map[string]any{"pattern": "**/*.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "main.go\npkg/a.go\npkg/deep/b_test.go\n" {
		t.Errorf("glob **/*.go =\n%s", res.Text)
	}
	res, err = (globTool{}).Run(context.Background(), env, in(map[string]any{"pattern": "*_test.go", "path": "pkg/deep"}))
	if err != nil || res.Text != "b_test.go\n" {
		t.Errorf("glob in a subdirectory = %q, %v", res.Text, err)
	}
	if _, err := (globTool{}).Run(context.Background(), env, in(map[string]any{"pattern": "[x"})); err == nil {
		t.Error("a malformed glob was accepted")
	}
	res, err = (listDir{}).Run(context.Background(), env, in(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != ".git/\nREADME.md\nblob.bin\nmain.go\npkg/\n" {
		t.Errorf("list_dir =\n%s", res.Text)
	}
}

func TestGlobMatch(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "a.go", true},
		{"*.go", "pkg/a.go", false},
		{"**/*.go", "a.go", true},
		{"**/*.go", "pkg/deep/a.go", true},
		{"pkg/**", "pkg/a/b", true},
		{"pkg/**/b", "pkg/b", true},
		{"pkg/**/b", "pkg/x/y/b", true},
		{"pkg/**/b", "pkg/x/y/c", false},
		{"a?c", "abc", true},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/b/c", false},
		{"", "a", false},
	} {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestGrepSaysWhereItStopped: a line longer than grep reads is reported, not skipped silently
// with everything after it.
func TestGrepSaysWhereItStopped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "long.txt"), "hit one\n"+strings.Repeat("x", 2<<20)+"\nhit two\n")
	res, err := (grepTool{}).Run(context.Background(), domain.Env{Cwd: dir}, in(map[string]any{"pattern": "hit"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "long.txt:1:hit one") || !strings.Contains(res.Text, "long.txt: [not searched past here") {
		t.Errorf("grep =\n%s", res.Text)
	}
}
