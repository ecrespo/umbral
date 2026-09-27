package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSizeReadsBytesAndBinarySuffixes covers the forms `[api] max_message_bytes` and
// `umb limits set --max-message` accept: bytes, or bytes with a KiB or MiB suffix.
func TestParseSizeReadsBytesAndBinarySuffixes(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]int{
		"4194304": 4 << 20,
		"4MiB":    4 << 20,
		"8 MiB":   8 << 20,
		"1536KiB": 1536 << 10,
		"64MiB":   64 << 20,
		// Past the ceiling is still a size: the range is ValidMaxMessageBytes's to refuse,
		// with a message that says so, not "not a size".
		"100MiB": 100 << 20,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "MiB", "4MB", "4GiB", "-1", "1.5MiB", "4 mib"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got)
		}
	}
}

// TestMaxMessageBytesIsReadAndBounded is the setting half of REQ-CLI-007: the default is
// 4 MiB, the file can change it within 1–64 MiB, and a value outside that stops the daemon
// like any other malformed setting.
func TestMaxMessageBytesIsReadAndBounded(t *testing.T) {
	t.Parallel()

	settings, err := LoadSettings(nil, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil || settings.MaxMessageBytes != DefaultMaxMessageBytes {
		t.Fatalf("no file: %d, %v; want the %d default", settings.MaxMessageBytes, err,
			DefaultMaxMessageBytes)
	}

	for body, want := range map[string]int{
		"[api]\nmax_message_bytes = 8388608\n":     8 << 20,
		"[api]\nmax_message_bytes = \"16MiB\"\n":   16 << 20,
		"[api]\nmax_message_bytes = \"1024KiB\"\n": 1 << 20,
		// A quoted value with a trailing comment, which the parser used to read as part of
		// the value: `16MiB"  # big` is no size, and the daemon refused to start.
		"[api]\nmax_message_bytes = \"16MiB\"  # big blocks\n": 16 << 20,
	} {
		settings, err := LoadSettings(nil, write(t, body))
		if err != nil || settings.MaxMessageBytes != want {
			t.Errorf("%q: %d, %v; want %d", body, settings.MaxMessageBytes, err, want)
		}
	}

	for _, body := range []string{
		"[api]\nmax_message_bytes = 1048575\n",
		"[api]\nmax_message_bytes = \"65MiB\"\n",
		"[api]\nmax_message_bytes = \"lots\"\n",
		"[api]\nmax_message_bytes = \"8MiB\n",
		"[api]\nmax_message_bytes = \"8MiB\" trailing\n",
		// An inline table is outside the subset. Read as an unknown key `api`, it started
		// the daemon at 4 MiB with 128 bytes asked for and out of range.
		"api = { max_message_bytes = 128 }\n",
	} {
		_, err := LoadSettings(nil, write(t, body))
		if !errors.Is(err, ErrSettingsInvalid) {
			t.Errorf("%q: %v, want ErrSettingsInvalid", body, err)
		}
	}
}

// TestWriteMaxMessageBytesCoversEveryFileCase is the file-cases table of delta
// `2026-09-frame-limit-monitoring`, one row at a time. The rewrite touches one line and
// keeps every other byte, because the file is the user's.
func TestWriteMaxMessageBytesCoversEveryFileCase(t *testing.T) {
	t.Parallel()

	const eight = 8 << 20

	t.Run("absent: created with [api] and the key only", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "nested", "config.toml")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got := read(t, path); got != "[api]\nmax_message_bytes = 8388608\n" {
			t.Errorf("created %q", got)
		}
	})

	t.Run("the key's line is replaced and every other line kept", func(t *testing.T) {
		t.Parallel()
		body := "# mine\n[experimental]\npane_history = true\n\n[api]\n" +
			"max_message_bytes = 4194304\n# after\n"
		path := write(t, body)
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(body, "4194304", "8388608", 1)
		if got := read(t, path); got != want {
			t.Errorf("rewrote to\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("a trailing comment is kept", func(t *testing.T) {
		t.Parallel()
		path := write(t, "[api]\n  max_message_bytes = \"4MiB\"   # raised for big blocks\n")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got, want := read(t, path),
			"[api]\n  max_message_bytes = 8388608   # raised for big blocks\n"; got != want {
			t.Errorf("rewrote to %q, want %q", got, want)
		}
	})

	t.Run("[api] without the key gains it under the header", func(t *testing.T) {
		t.Parallel()
		path := write(t, "[api]\n\n[experimental]\npane_history = false\n")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got, want := read(t, path),
			"[api]\nmax_message_bytes = 8388608\n\n[experimental]\npane_history = false\n"; got != want {
			t.Errorf("rewrote to %q, want %q", got, want)
		}
	})

	t.Run("no [api] section: appended, even after a missing final newline", func(t *testing.T) {
		t.Parallel()
		path := write(t, "[experimental]\npane_history = true")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got, want := read(t, path),
			"[experimental]\npane_history = true\n\n[api]\nmax_message_bytes = 8388608\n"; got != want {
			t.Errorf("rewrote to %q, want %q", got, want)
		}
	})

	for name, body := range map[string]string{
		"does not parse: refused, untouched":     "[api\nmax_message_bytes = 1\n",
		"dotted key: refused, untouched":         "api.max_message_bytes = 4194304\n",
		"inline table: refused, untouched":       "api = { max_message_bytes = 4194304 }\n",
		"a value past the ceiling never written": "[api]\nmax_message_bytes = 4194304\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := write(t, body)
			value := eight
			if strings.HasPrefix(name, "a value past") {
				value = MaxMaxMessageBytes + 1
			}
			err := WriteMaxMessageBytes(path, value)
			if err == nil {
				t.Fatalf("accepted; the file is now %q", read(t, path))
			}
			if got := read(t, path); got != body {
				t.Errorf("a refused write changed the file to %q", got)
			}
			if strings.HasPrefix(name, "a value past") {
				if !errors.Is(err, ErrSizeOutOfRange) {
					t.Errorf("error = %v, want ErrSizeOutOfRange", err)
				}
				return
			}
			if !errors.Is(err, ErrSettingsInvalid) {
				t.Errorf("error = %v, want ErrSettingsInvalid", err)
			}
			if !strings.Contains(name, "does not parse") && !strings.Contains(err.Error(), "line ") {
				t.Errorf("the refusal %q does not name the line", err)
			}
		})
	}

	t.Run("CRLF line endings are kept", func(t *testing.T) {
		t.Parallel()
		path := write(t, "[api]\r\nmax_message_bytes = 4194304  # why\r\n[x]\r\n")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got, want := read(t, path), "[api]\r\nmax_message_bytes = 8388608  # why\r\n[x]\r\n"; got != want {
			t.Errorf("rewrote to %q, want %q", got, want)
		}
		path = write(t, "[api]\r\n[x]\r\n")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got, want := read(t, path), "[api]\r\nmax_message_bytes = 8388608\r\n[x]\r\n"; got != want {
			t.Errorf("inserted as %q, want %q", got, want)
		}
		path = write(t, "[x]\r\n")
		if err := WriteMaxMessageBytes(path, eight); err != nil {
			t.Fatal(err)
		}
		if got, want := read(t, path), "[x]\r\n\r\n[api]\r\nmax_message_bytes = 8388608\r\n"; got != want {
			t.Errorf("appended as %q, want %q", got, want)
		}
	})

	t.Run("a symlink survives: the target is rewritten", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "dotfiles", "umbral.toml")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("[api]\nmax_message_bytes = 4194304\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "config.toml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		if err := WriteMaxMessageBytes(link, eight); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was replaced by a regular file (%v)", err)
		}
		if got := read(t, target); got != "[api]\nmax_message_bytes = 8388608\n" {
			t.Errorf("target is %q", got)
		}
		leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(target), "*.tmp*"))
		if len(leftovers) != 0 {
			t.Errorf("temporary files left beside the link: %v", leftovers)
		}
	})
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- a test's own temporary file
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
