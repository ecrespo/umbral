package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The frame limit's range (API Spec §8, delta `2026-09-frame-limit-monitoring`). The floor
// keeps a user from configuring a daemon that cannot answer an ordinary `block.get`; the
// ceiling bounds what one authenticated connection can make either side buffer.
const (
	DefaultMaxMessageBytes = 4 << 20
	MinMaxMessageBytes     = 1 << 20
	MaxMaxMessageBytes     = 64 << 20
)

// ErrSettingsInvalid marks a settings file the daemon will not act on: it does not parse, a
// value is malformed, or a key is written in a form `WriteMaxMessageBytes` does not edit.
// `internal/api` maps it to CONFIG_INVALID.
var ErrSettingsInvalid = errors.New("the settings file is invalid")

// ErrSizeOutOfRange is a frame limit outside MinMaxMessageBytes..MaxMaxMessageBytes.
var ErrSizeOutOfRange = errors.New("the frame limit is out of range")

// sizeSuffixes are the units a size may carry. Binary units only, spelled exactly: "4MB"
// is refused rather than read as MiB, because a user who wrote decimal megabytes meant a
// different number, and guessing which is the silent fallback this package avoids.
var sizeSuffixes = []struct {
	suffix string
	shift  uint
}{{"MiB", 20}, {"KiB", 10}}

// maxParsedSize bounds what ParseSize reads, only so a shift cannot overflow. The frame
// limit's range is ValidMaxMessageBytes's to enforce, with a message that names it: "100MiB"
// is a size, and an out-of-range one.
const maxParsedSize = 1 << 40

// ParseSize reads a size in bytes, or with a KiB or MiB suffix ("8MiB", "8 MiB",
// "8388608"). It does not check the range; ValidMaxMessageBytes does.
func ParseSize(text string) (int, error) {
	number, shift := strings.TrimSpace(text), uint(0)
	for _, s := range sizeSuffixes {
		if trimmed, found := strings.CutSuffix(number, s.suffix); found {
			number, shift = strings.TrimSpace(trimmed), s.shift
			break
		}
	}
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 || n > maxParsedSize>>shift {
		return 0, fmt.Errorf("%q is not a size: want bytes, or a number with KiB or MiB", text)
	}
	return n << shift, nil
}

// ValidMaxMessageBytes reports whether n is a frame limit the daemon accepts.
func ValidMaxMessageBytes(n int) error {
	if n < MinMaxMessageBytes || n > MaxMaxMessageBytes {
		return fmt.Errorf("%w: %d bytes; it must be between %d (1 MiB) and %d (64 MiB)",
			ErrSizeOutOfRange, n, MinMaxMessageBytes, MaxMaxMessageBytes)
	}
	return nil
}

// parseMaxMessageBytes reads the setting's value as the settings file holds it: an integer,
// or a quoted size with a suffix (a bare `8MiB` is not TOML).
func parseMaxMessageBytes(value string) (int, error) {
	n, err := ParseSize(value)
	if err != nil {
		return 0, err
	}
	if err := ValidMaxMessageBytes(n); err != nil {
		return 0, err
	}
	return n, nil
}

// maxMessageKey is the one line WriteMaxMessageBytes edits.
const (
	apiSection    = "api"
	maxMessageKey = "max_message_bytes"
)

// WriteMaxMessageBytes sets `[api] max_message_bytes` in the settings file at path, which is
// what `limits.set` persists (REQ-CLI-007).
//
// It is an edit to one known key, not a TOML writer, and the delta's file-cases table is
// its whole contract:
//
//   - an absent file is created holding `[api]` and the key and nothing else;
//   - a file that does not parse is refused, untouched;
//   - the key written as a dotted key is refused, naming the line, because rewriting another
//     form correctly is a TOML writer's job; an inline table never gets that far, since the
//     parser refuses it as outside the subset, also naming the line;
//   - the key's own line is rewritten keeping its indentation and any trailing comment, and
//     every other line is kept byte for byte;
//   - a symlink is resolved and its target rewritten, so a dotfile manager's link survives.
//
// The write is atomic: a temporary file beside the target, then a rename, so a daemon
// autostarted mid-write never reads half a file.
func WriteMaxMessageBytes(path string, n int) error {
	if err := ValidMaxMessageBytes(n); err != nil {
		return err
	}
	target, err := resolveLink(path)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(target) // #nosec G304 -- the daemon's own settings file
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("config: create %s: %w", filepath.Dir(target), err)
		}
		return writeAtomically(target, fmt.Sprintf("[%s]\n%s = %d\n", apiSection, maxMessageKey, n), 0o600)
	case err != nil:
		return fmt.Errorf("config: read %s: %w", target, err)
	}

	if _, err := parseTOMLSubset(string(raw)); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, target, err)
	}
	updated, err := setMaxMessageLine(string(raw), n)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, target, err)
	}

	mode := fs.FileMode(0o600)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	return writeAtomically(target, updated, mode)
}

// setMaxMessageLine returns text with the key set to n. text has already parsed.
func setMaxMessageLine(text string, n int) (string, error) {
	// A file written with CRLF keeps them on the lines this edit writes too: "every other
	// line byte for byte" is not worth much if the one line it touches changes its ending.
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	cr := strings.TrimSuffix(eol, "\n")

	lines := strings.Split(text, "\n")
	section, headerAt, keyAt := "", -1, -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if section == apiSection {
				headerAt = i
			}
			continue
		}
		key, _, _ := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		switch {
		case section == "" && key == apiSection+"."+maxMessageKey:
			return "", fmt.Errorf("line %d writes %s.%s as a dotted key; edit that line by hand",
				i+1, apiSection, maxMessageKey)
		case section == apiSection && key == maxMessageKey:
			keyAt = i
		}
	}

	value := strconv.Itoa(n)
	switch {
	case keyAt >= 0:
		line, hadCR := strings.CutSuffix(lines[keyAt], "\r")
		lines[keyAt] = replaceValue(line, value)
		if hadCR {
			lines[keyAt] += "\r"
		}
	case headerAt >= 0:
		lines = append(lines[:headerAt+1],
			append([]string{maxMessageKey + " = " + value + cr}, lines[headerAt+1:]...)...)
	default:
		// No [api] section: appended after a blank line, and after a newline first if the
		// file's last line has none, so no existing byte moves.
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += eol
		}
		if strings.TrimSpace(text) != "" {
			text += eol
		}
		return text + "[" + apiSection + "]" + eol + maxMessageKey + " = " + value + eol, nil
	}
	return strings.Join(lines, "\n"), nil
}

// replaceValue swaps the value on a `key = value  # comment` line, keeping everything around
// it: the indentation and key before the `=`, and the whitespace and comment after the value.
func replaceValue(line, value string) string {
	before, after, _ := strings.Cut(line, "=")
	rest := strings.TrimLeft(after, " \t")
	comment := ""
	if !strings.HasPrefix(rest, `"`) {
		if i := strings.Index(rest, "#"); i >= 0 {
			comment = rest[i:]
			rest = rest[:i]
		}
	} else if end := strings.Index(rest[1:], `"`); end >= 0 {
		tail := rest[end+2:]
		if i := strings.Index(tail, "#"); i >= 0 {
			comment = tail[i:]
			rest = rest[:end+2] + tail[:i]
		}
	}
	gap := rest[len(strings.TrimRight(rest, " \t")):]
	if comment == "" {
		gap = ""
	}
	return strings.TrimRight(before, " \t") + " = " + value + gap + comment
}

// resolveLink returns the file a settings path names: its final target if it is a symlink,
// itself otherwise, including when nothing is there yet.
func resolveLink(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("config: resolve %s: %w", path, err)
	}
	// Absent, or a link whose target is absent: follow the link by hand so the file is
	// created where it points, not over it.
	if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		dest, rerr := os.Readlink(path)
		if rerr != nil {
			return "", fmt.Errorf("config: read the link %s: %w", path, rerr)
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		return dest, nil
	}
	return path, nil
}

// writeAtomically replaces path with body through a temporary file in the same directory.
func writeAtomically(path, body string, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once the rename has happened

	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}
