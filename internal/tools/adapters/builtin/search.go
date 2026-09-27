package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
)

// Limits on what a search returns, so one call cannot fill a model's context.
const (
	maxMatches    = 500
	maxEntries    = 1000
	maxLineChars  = 300
	maxSearchFile = 4 << 20
)

// errEnough stops a walk once a limit is reached.
var errEnough = errors.New("enough")

// skipDir is a directory no search descends into.
func skipDir(name string) bool { return name == ".git" }

func readOnlyAction(env domain.Env, tool, path string) secdomain.Action {
	return secdomain.Action{ThreadID: env.ThreadID, Tool: tool, Risk: secdomain.RiskReadOnly, Target: target(env, path)}
}

// grepTool is grep: a regular expression over the text files under a directory.
type grepTool struct{}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
}

func (grepTool) Spec() domain.Spec {
	return domain.Spec{
		Name: "grep",
		Description: "Search the text files under a directory (or one file) for a regular expression (Go RE2 " +
			"syntax). Prints path:line:text, at most 500 matches.",
		Risk: secdomain.RiskReadOnly,
		InputSchema: object([]string{fPattern}, map[string]any{
			fPattern:      nonEmpty("Regular expression."),
			fPath:         str("Directory or file to search; the working directory by default."),
			"glob":        str("Only files whose path relative to the search root matches this glob, e.g. **/*.go."),
			"ignore_case": map[string]any{kType: "boolean"},
		}),
	}
}

func (grepTool) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in grepInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return readOnlyAction(env, "grep", in.Path), nil
}

func (grepTool) Summary(input json.RawMessage) string {
	var in grepInput
	_ = json.Unmarshal(input, &in)
	return "grep " + in.Pattern
}

func (grepTool) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	var in grepInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	expr := in.Pattern
	if in.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return domain.Result{}, fmt.Errorf("%w: pattern: %w", domain.ErrInvalidInput, err)
	}
	root := resolve(env, in.Path)
	var out []string
	err = walkFiles(ctx, root, in.Glob, func(rel, full string) error {
		if !grepFile(re, rel, full, &out) {
			return errEnough
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return domain.Result{}, err
	}
	return listing("grep "+in.Pattern, out, len(out) >= maxMatches, "matches"), nil
}

// grepFile appends the matches of one file to out, skipping a file it cannot read or that is
// not text. It reports false once out is full.
func grepFile(re *regexp.Regexp, rel, full string, out *[]string) bool {
	f, err := openRegular(full)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 8000)
	n, _ := f.Read(head)
	if binary(head[:n]) {
		return true
	}
	if _, err := f.Seek(0, 0); err != nil {
		return true
	}
	// The size was checked when the walk saw the file; the cap holds if it grew since, or
	// is a pseudo-file that reports no size.
	sc := bufio.NewScanner(io.LimitReader(f, maxSearchFile))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for line := 1; sc.Scan(); line++ {
		if !re.MatchString(sc.Text()) {
			continue
		}
		*out = append(*out, fmt.Sprintf("%s:%d:%s", rel, line, clip(sc.Text())))
		if len(*out) >= maxMatches {
			return false
		}
	}
	if err := sc.Err(); err != nil {
		*out = append(*out, fmt.Sprintf("%s: [not searched past here: %v]", rel, err))
	}
	return len(*out) < maxMatches
}

// clip shortens a line to maxLineChars bytes without splitting a character.
func clip(s string) string {
	if len(s) <= maxLineChars {
		return s
	}
	cut := maxLineChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// walkFiles calls fn for every regular file under root whose slash path relative to root
// matches pattern ("" matches all), skipping .git. A root that is a file is walked alone.
func walkFiles(ctx context.Context, root, pattern string, fn func(rel, full string) error) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() || info.Size() > maxSearchFile {
			return fmt.Errorf("%s is not a regular file of at most %d MiB", root, maxSearchFile>>20)
		}
		return fn(filepath.Base(root), root)
	}
	return filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			// An entry that cannot be read is left out; the search goes on.
			return unreadable(d)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if full != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, full)
		rel = filepath.ToSlash(rel)
		if pattern != "" && !globMatch(pattern, rel) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Size() <= maxSearchFile {
			return fn(rel, full)
		}
		return nil
	})
}

// unreadable is what a walk does with an entry it cannot read: skip it, and skip a
// directory's contents.
func unreadable(d fs.DirEntry) error {
	if d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// globMatch matches a slash path against a glob where `*`, `?` and `[…]` work within one
// segment and `**` matches any number of segments, none included.
func globMatch(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// listing is a result that is a list of lines, with a note when a limit cut it.
func listing(summary string, lines []string, cut bool, what string) domain.Result {
	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	if cut {
		text += fmt.Sprintf("[stopped at %d %s: narrow the search]\n", len(lines), what)
	}
	if len(lines) == 0 {
		text = "no " + what + "\n"
	}
	return domain.Result{Summary: fmt.Sprintf("%s: %d %s", summary, len(lines), what), Text: text}
}

// globTool is glob: the files under a directory whose relative path matches a pattern.
type globTool struct{}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (globTool) Spec() domain.Spec {
	return domain.Spec{
		Name:        "glob",
		Description: "List the files under a directory whose path relative to it matches a glob; ** spans directories. At most 1000.",
		Risk:        secdomain.RiskReadOnly,
		InputSchema: object([]string{fPattern}, map[string]any{
			fPattern: nonEmpty("Glob, e.g. **/*_test.go."),
			fPath:    str("Directory to search; the working directory by default."),
		}),
	}
}

func (globTool) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in globInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return readOnlyAction(env, "glob", in.Path), nil
}

func (globTool) Summary(input json.RawMessage) string {
	var in globInput
	_ = json.Unmarshal(input, &in)
	return "glob " + in.Pattern
}

func (globTool) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	var in globInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	if _, err := path.Match(strings.ReplaceAll(in.Pattern, "**", "*"), ""); err != nil {
		return domain.Result{}, fmt.Errorf("%w: pattern: %w", domain.ErrInvalidInput, err)
	}
	var out []string
	err := walkFiles(ctx, resolve(env, in.Path), in.Pattern, func(rel, _ string) error {
		out = append(out, rel)
		if len(out) >= maxEntries {
			return errEnough
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return domain.Result{}, err
	}
	sort.Strings(out)
	return listing("glob "+in.Pattern, out, len(out) >= maxEntries, "files"), nil
}

// listDir is list_dir: one directory's entries, directories marked with a trailing slash.
type listDir struct{}

type listInput struct {
	Path string `json:"path"`
}

func (listDir) Spec() domain.Spec {
	return domain.Spec{
		Name:        "list_dir",
		Description: "List a directory's entries, directories ending in /. At most 1000.",
		Risk:        secdomain.RiskReadOnly,
		InputSchema: object(nil, map[string]any{
			fPath: str("Directory; the working directory by default."),
		}),
	}
}

func (listDir) Action(env domain.Env, input json.RawMessage) (secdomain.Action, error) {
	var in listInput
	if err := decode(input, &in); err != nil {
		return secdomain.Action{}, err
	}
	return readOnlyAction(env, "list_dir", in.Path), nil
}

func (listDir) Summary(input json.RawMessage) string {
	var in listInput
	_ = json.Unmarshal(input, &in)
	return "list " + in.Path
}

func (listDir) Run(_ context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	var in listInput
	if err := decode(input, &in); err != nil {
		return domain.Result{}, err
	}
	entries, err := os.ReadDir(resolve(env, in.Path))
	if err != nil {
		return domain.Result{}, err
	}
	out := make([]string, 0, min(len(entries), maxEntries))
	for _, e := range entries {
		if len(out) >= maxEntries {
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return listing("list "+in.Path, out, len(entries) > maxEntries, "entries"), nil
}
