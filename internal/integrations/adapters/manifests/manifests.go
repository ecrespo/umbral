// Package manifests reads local agent detection manifests, `$XDG_CONFIG_HOME/umbral/agents/
// *.toml`, which override the built-in ones by name (REQ-XAG-002).
package manifests

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ecrespo/umbral/internal/integrations/domain"
)

// maxFileBytes bounds one manifest file.
const maxFileBytes = 64 << 10

// file is a manifest file's shape. Unknown keys make the file invalid, so a misspelled rule is
// reported instead of silently matching nothing.
type file struct {
	Name        string `toml:"name"`
	DisplayName string `toml:"display_name"`
	StateSource string `toml:"state_source"`
	Detect      struct {
		Executables []string `toml:"executables"`
		Argv        []string `toml:"argv"`
	} `toml:"detect"`
}

// Load reads every `*.toml` in dir in lexical order. A file that cannot be read, parsed or
// validated is skipped whole and returned among the skipped ones; two files naming one agent
// keep the later. A missing dir is no manifests and no error.
func Load(dir string) (manifests []domain.Manifest, skipped []domain.SkippedManifest) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, []domain.SkippedManifest{{File: dir, Reason: err.Error()}}
	}
	sort.Strings(paths)
	return LoadFiles(paths)
}

// LoadFiles reads the given manifest files in order, as Load reads a directory's: a file that
// fails is skipped whole, and two naming one agent keep the later. Installed plugins' detection
// manifests are read this way (delta `2026-10-plugins`, decision 7).
func LoadFiles(paths []string) (manifests []domain.Manifest, skipped []domain.SkippedManifest) {
	byName := map[string]int{}
	for _, p := range paths {
		m, err := read(p)
		if err != nil {
			skipped = append(skipped, domain.SkippedManifest{File: p, Reason: err.Error()})
			continue
		}
		if i, ok := byName[m.Name]; ok {
			manifests[i] = m
			continue
		}
		byName[m.Name] = len(manifests)
		manifests = append(manifests, m)
	}
	return manifests, skipped
}

// read loads one file. A symlink is followed, as dotfile managers make them; what it names must
// be a regular file. The size is bounded while reading, so a file that grows after the check is
// still refused.
func read(path string) (domain.Manifest, error) {
	// Checked before opening too: opening a FIFO would block the daemon's start.
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return domain.Manifest{}, fmt.Errorf("%w: not a regular file", domain.ErrInvalidManifest)
	}
	fh, err := os.Open(path) //nolint:gosec // the user's own configuration directory
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("%w: cannot be read", domain.ErrInvalidManifest)
	}
	defer func() { _ = fh.Close() }()
	if info, err := fh.Stat(); err != nil || !info.Mode().IsRegular() {
		return domain.Manifest{}, fmt.Errorf("%w: not a regular file", domain.ErrInvalidManifest)
	}
	raw, err := io.ReadAll(io.LimitReader(fh, maxFileBytes+1))
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("%w: cannot be read", domain.ErrInvalidManifest)
	}
	return Parse(raw, path)
}

// Parse reads one manifest's bytes; origin names where they came from. A plugin's detection
// manifests are checked with it at inspect (delta `2026-10-plugins`, decision 2).
func Parse(raw []byte, origin string) (domain.Manifest, error) {
	if len(raw) > maxFileBytes {
		return domain.Manifest{}, fmt.Errorf("%w: larger than %d bytes", domain.ErrInvalidManifest, maxFileBytes)
	}
	var f file
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return domain.Manifest{}, fmt.Errorf("%w: %s", domain.ErrInvalidManifest, unknownKeys(strict))
		}
		// Neither a syntax error's rendering nor a type mismatch's message is passed on: the first
		// quotes the file's lines and the second names Go internals.
		var decode *toml.DecodeError
		if errors.As(err, &decode) {
			line, _ := decode.Position()
			return domain.Manifest{}, fmt.Errorf("%w: not valid TOML at line %d", domain.ErrInvalidManifest, line)
		}
		return domain.Manifest{}, fmt.Errorf("%w: a field has the wrong type", domain.ErrInvalidManifest)
	}
	return domain.Manifest{
		Name: f.Name, DisplayName: f.DisplayName, StateSource: f.StateSource,
		Executables: f.Detect.Executables, Argv: f.Detect.Argv, Origin: origin,
	}.Compile()
}

// unknownKeys names each unknown key and its line, and nothing else from the file: go-toml's
// own rendering quotes the surrounding lines, and those can hold a value that belongs in no log.
func unknownKeys(strict *toml.StrictMissingError) string {
	keys := make([]string, 0, len(strict.Errors))
	for i := range strict.Errors {
		line, _ := strict.Errors[i].Position()
		keys = append(keys, fmt.Sprintf("unknown key %q at line %d", strings.Join(strict.Errors[i].Key(), "."), line))
	}
	return strings.Join(keys, "; ")
}
