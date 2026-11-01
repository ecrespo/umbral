package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Errors of the skill store (delta `2026-09-skills-cli`); internal/api maps them to the wire.
var (
	ErrSkillValidation = errors.New("invalid skill")
	ErrSkillConflict   = errors.New("skill conflict")
	ErrSkillNotFound   = errors.New("skill not found")
)

// The bundle checks and the store's limits (delta `2026-09-skills-cli`, decisions 1 and 5).
const (
	SkillFileName          = "SKILL.md"
	MaxSkillFileBytes      = 1 << 20
	MaxSkillBundleBytes    = 8 << 20
	MaxSkillEntries        = 256
	MaxEnabledSkills       = 64
	MaxSkillNameLen        = 64
	MaxSkillDescriptionLen = 1024
	MaxSkillVersionLen     = 64
	// MaxCatalogDescription is how much of a description the prompt's catalog carries
	// (REQ-SKL-004), in characters.
	MaxCatalogDescription = 256
)

// The changes skill.changed announces.
const (
	SkillInstalled = "installed"
	SkillReplaced  = "replaced"
	SkillEnabled   = "enabled"
	SkillDisabled  = "disabled"
	SkillRemoved   = "removed"
)

// Skill is an installed skill, API §4's `Skill`.
type Skill struct {
	ID, Name, Description, Version string
	// Source is the path it was installed from, for display only: the stored copy is used.
	Source      string
	SHA256      string
	SizeBytes   int64
	Enabled     bool
	InstalledAt int64
}

// SkillFile is one file of a bundle, as a manifest lists it.
type SkillFile struct {
	Path string
	Size int64
}

// Manifest is what skill.inspect shows before anything is copied.
type Manifest struct {
	Name, Description, Version string
	Files                      []SkillFile
	SizeBytes                  int64
	SHA256                     string
}

// InstallParams is skill.install's request.
type InstallParams struct {
	Path           string
	ExpectedSHA256 string
	Replace        bool
}

// Bundle is a checked bundle held in memory: its manifest and its files' contents. Its size
// is bounded by MaxSkillBundleBytes, which is why holding it is safe.
type Bundle struct {
	Manifest
	Contents map[string][]byte
	// Body is SKILL.md after its front matter.
	Body string
}

var scpLike = regexp.MustCompile(`^[^/@]+@[^/:]+:`)

// IsRemoteSource classifies an install source before any path is resolved: a URL of any
// scheme, scp-like `user@host:path` and a `git@` remote are not local (decision 4). Without
// classifying first, `https://x` would be made absolute and come back as "not found".
func IsRemoteSource(src string) bool {
	return strings.Contains(src, "://") || strings.HasPrefix(src, "git@") || scpLike.MatchString(src)
}

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidSkillName checks a name: 1–64 of [a-z0-9-], not starting with '-'.
func ValidSkillName(name string) error {
	if len(name) == 0 || len(name) > MaxSkillNameLen || !skillName.MatchString(name) {
		return fmt.Errorf("%w: name %q must be 1-%d characters of a-z, 0-9 and '-', not starting with '-'",
			ErrSkillValidation, name, MaxSkillNameLen)
	}
	return nil
}

// BundleBuilder assembles a bundle entry by entry and enforces the checks as it goes, so a
// tarball is refused at the first byte past a limit rather than after it has been read.
type BundleBuilder struct {
	contents map[string][]byte
	entries  int
	total    int64
}

// NewBundleBuilder starts an empty bundle.
func NewBundleBuilder() *BundleBuilder { return &BundleBuilder{contents: map[string][]byte{}} }

// CleanEntry checks an entry's path and returns it slash-separated and clean: not empty, not
// absolute, no `..` component.
func CleanEntry(name string) (string, error) {
	if strings.Contains(name, "\\") {
		// Refused rather than rewritten: on Linux `a\b` is one name, and installing it as
		// `a/b` would install something other than what the user saw.
		return "", fmt.Errorf("%w: entry %q: a backslash in a name", ErrSkillValidation, name)
	}
	slashed := name
	if slashed == "" || strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("%w: entry %q: an absolute or empty path", ErrSkillValidation, name)
	}
	for _, part := range strings.Split(strings.TrimSuffix(slashed, "/"), "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: entry %q: a '..' component", ErrSkillValidation, name)
		}
	}
	clean := path.Clean(slashed)
	if clean == "." {
		return "", nil // the bundle's root
	}
	return clean, nil
}

func (b *BundleBuilder) count(name string) error {
	b.entries++
	if b.entries > MaxSkillEntries {
		return fmt.Errorf("%w: entry %q: more than %d entries", ErrSkillValidation, name, MaxSkillEntries)
	}
	return nil
}

// AddDir records a directory entry. Directories count against the entry limit; they carry no
// content and are not part of the digest.
func (b *BundleBuilder) AddDir(name string) error {
	clean, err := CleanEntry(name)
	if err != nil || clean == "" {
		return err
	}
	return b.count(name)
}

// AddFile reads one regular file, never more than one byte past the limits.
func (b *BundleBuilder) AddFile(name string, r io.Reader) error {
	clean, err := CleanEntry(name)
	if err != nil {
		return err
	}
	if clean == "" {
		return fmt.Errorf("%w: entry %q: a file at the bundle's root path", ErrSkillValidation, name)
	}
	if _, dup := b.contents[clean]; dup {
		return fmt.Errorf("%w: entry %q: appears twice", ErrSkillValidation, name)
	}
	if err := b.count(name); err != nil {
		return err
	}
	// Never more than one byte past whichever limit is nearer: the file's or what is left
	// of the bundle's.
	limit := min(int64(MaxSkillFileBytes), MaxSkillBundleBytes-b.total)
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return fmt.Errorf("%w: entry %q: %w", ErrSkillValidation, name, err)
	}
	switch {
	case len(data) > MaxSkillFileBytes:
		return fmt.Errorf("%w: entry %q: larger than 1 MiB", ErrSkillValidation, name)
	case int64(len(data)) > limit:
		return fmt.Errorf("%w: entry %q: the bundle is larger than 8 MiB", ErrSkillValidation, name)
	}
	b.total += int64(len(data))
	b.contents[clean] = data
	return nil
}

// Files checks the bundle as a whole and returns its files, which must hold root at the
// bundle's root. A tarball whose entries all sit under one top directory holding root, as
// `tar czf x.tar.gz my-skill/` makes, is read as that directory. A plugin's bundle is read
// with its own root file (delta `2026-10-plugins`, decision 1).
func (b *BundleBuilder) Files(root string) (map[string][]byte, error) {
	contents := b.contents
	if _, ok := contents[root]; !ok {
		contents = stripTopDir(contents, root)
	}
	for p := range contents {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if _, isFile := contents[d]; isFile {
				return nil, fmt.Errorf("%w: entry %q: under %q, which is a file", ErrSkillValidation, p, d)
			}
		}
	}
	if _, ok := contents[root]; !ok {
		return nil, fmt.Errorf("%w: the bundle has no %s at its root", ErrSkillValidation, root)
	}
	return contents, nil
}

// Finish checks the bundle as a whole and builds its manifest from its SKILL.md.
func (b *BundleBuilder) Finish() (Bundle, error) {
	contents, err := b.Files(SkillFileName)
	if err != nil {
		return Bundle{}, err
	}
	md := contents[SkillFileName]
	fm, body, err := ParseFrontMatter(string(md))
	if err != nil {
		return Bundle{}, err
	}
	m := Manifest{Name: fm.Name, Description: fm.Description, Version: fm.Version}
	for p, data := range contents {
		m.Files = append(m.Files, SkillFile{Path: p, Size: int64(len(data))})
		m.SizeBytes += int64(len(data))
	}
	slices.SortFunc(m.Files, func(a, b SkillFile) int { return strings.Compare(a.Path, b.Path) })
	m.SHA256 = Digest(contents)
	return Bundle{Manifest: m, Contents: contents, Body: body}, nil
}

func stripTopDir(contents map[string][]byte, root string) map[string][]byte {
	top := ""
	for p := range contents {
		first, _, nested := strings.Cut(p, "/")
		if !nested || (top != "" && first != top) {
			return contents
		}
		top = first
	}
	if _, ok := contents[top+"/"+root]; top == "" || !ok {
		return contents
	}
	out := make(map[string][]byte, len(contents))
	for p, data := range contents {
		out[strings.TrimPrefix(p, top+"/")] = data
	}
	return out
}

// Digest is the SHA-256 of the canonical manifest: the files sorted by path, each written as
// `path NUL size NUL sha256(content) LF`. The same files give the same digest whether they
// came from a directory or a tarball, whatever their modes or order.
func Digest(contents map[string][]byte) string {
	paths := make([]string, 0, len(contents))
	for p := range contents {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	h := sha256.New()
	for _, p := range paths {
		sum := sha256.Sum256(contents[p])
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\n", p, len(contents[p]), hex.EncodeToString(sum[:])) // a hash never fails to write
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FrontMatter is SKILL.md's YAML header.
type FrontMatter struct {
	Name, Description, Version string
}

// ParseFrontMatter reads SKILL.md's front matter: a `---` line, `key: value` lines and a
// closing `---`. Values may be plain, quoted, or a `>`/`|` block of indented lines; keys other
// than name, description and version are ignored. It is the subset skills use, read without
// a YAML library because Art. 3 keeps the domain on the standard library.
func ParseFrontMatter(md string) (FrontMatter, string, error) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	rest, ok := strings.CutPrefix(md, "---\n")
	if !ok {
		return FrontMatter{}, "", fmt.Errorf("%w: %s must start with a '---' front matter line", ErrSkillValidation, SkillFileName)
	}
	header, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if h, found := strings.CutSuffix(rest, "\n---"); found {
			header, body, ok = h, "", true
		}
	}
	if !ok {
		return FrontMatter{}, "", fmt.Errorf("%w: %s's front matter has no closing '---' line", ErrSkillValidation, SkillFileName)
	}
	values := map[string]string{}
	lines := strings.Split(header, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return FrontMatter{}, "", fmt.Errorf("%w: front matter line %q is not 'key: value'", ErrSkillValidation, line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if _, dup := values[key]; dup {
			return FrontMatter{}, "", fmt.Errorf("%w: front matter key %q appears twice", ErrSkillValidation, key)
		}
		if strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|") {
			sep := " "
			if value[0] == '|' {
				sep = "\n"
			}
			var block []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			value = strings.TrimSpace(strings.Join(block, sep))
		} else {
			value = unquote(value)
		}
		values[key] = value
	}
	fm := FrontMatter{Name: values["name"], Description: values["description"], Version: values["version"]}
	if err := ValidSkillName(fm.Name); err != nil {
		return FrontMatter{}, "", err
	}
	if n := utf8.RuneCountInString(fm.Description); n == 0 || n > MaxSkillDescriptionLen {
		return FrontMatter{}, "", fmt.Errorf("%w: description must be 1-%d characters, not %d", ErrSkillValidation, MaxSkillDescriptionLen, n)
	}
	if utf8.RuneCountInString(fm.Version) > MaxSkillVersionLen {
		return FrontMatter{}, "", fmt.Errorf("%w: version is longer than %d characters", ErrSkillValidation, MaxSkillVersionLen)
	}
	return fm, body, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '"' && v[len(v)-1] == '"':
			return strings.ReplaceAll(strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`), `\\`, `\`)
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
	}
	return v
}

// CatalogEntry is one enabled skill as each turn's prompt lists it (REQ-SKL-004): its name and
// the start of its description, never its body.
type CatalogEntry struct {
	Name        string
	Description string
}

// Catalog is the prompt's view of the installed skills: the enabled ones, in the order given,
// at most MaxEnabledSkills, each description made one line and cut to MaxCatalogDescription
// characters.
func Catalog(skills []Skill) []CatalogEntry {
	out := []CatalogEntry{}
	for _, sk := range skills {
		if !sk.Enabled || len(out) == MaxEnabledSkills {
			continue
		}
		d := oneLine(sk.Description)
		if r := []rune(d); len(r) > MaxCatalogDescription {
			d = string(r[:MaxCatalogDescription])
		}
		out = append(out, CatalogEntry{Name: sk.Name, Description: d})
	}
	return out
}

// oneLine makes a description one line of plain text for the system prompt. A `|` block keeps
// its newlines, and a description is the bundle's own text: left as it is, it could open a
// section of the prompt — a fake `# Git`, a closing `</rules>` — that the model would read as
// Umbral's. Every run of whitespace becomes one space, and control and format characters (bidi
// overrides among them) are dropped.
func oneLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
