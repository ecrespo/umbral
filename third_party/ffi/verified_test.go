package ffi

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests are Umbral's (T-HRD-18): the copy of this module in third_party/ffi loads only a
// libffi whose bytes it has checked.

var genuine = []byte("the genuine library's bytes, as embedded in the binary")

// loaded fails unless path holds exactly the genuine bytes, as a regular file.
func loaded(t *testing.T, path string) {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatalf("the library would be loaded by a name the system resolves: %q", path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file: %v", path, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, genuine) {
		t.Fatalf("%s does not hold the embedded bytes (%d bytes, %v)", path, len(got), err)
	}
}

// TestACleanCacheGetsTheEmbeddedLibrary: the first start writes the library to the cache and
// loads it from there; the second finds it, checks it and writes nothing.
func TestACleanCacheGetsTheEmbeddedLibrary(t *testing.T) {
	base := t.TempDir()
	path, err := verifiedLibrary(base, "libffi.test", genuine)
	if err != nil {
		t.Fatal(err)
	}
	loaded(t, path)
	if want := filepath.Join(base, cacheDir, libffiVersion, "libffi.test"); path != want {
		t.Errorf("loaded from %s, want the cache path %s", path, want)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := verifiedLibrary(base, "libffi.test", genuine)
	if err != nil || again != path {
		t.Fatalf("the second start: %q, %v", again, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("a cached library that checks out was written again")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Errorf("the cache holds more than the library: %v %v", entries, err)
	}
}

// TestAPlantedLibraryIsNeverLoaded: whatever a process of the same user left at the cache
// path — another library, a truncated one, a longer one, a symbolic link to another file or
// to the genuine bytes elsewhere, a directory — what is loaded holds the embedded bytes and
// is a regular file; the planted file's bytes are never the ones returned.
func TestAPlantedLibraryIsNeverLoaded(t *testing.T) {
	plant := map[string]func(t *testing.T, dest string){
		"another library": func(t *testing.T, dest string) {
			write(t, dest, []byte("code of someone else's, the same length as the genuine!"))
		},
		"same size, one byte off": func(t *testing.T, dest string) {
			evil := bytes.Clone(genuine)
			evil[len(evil)-1] ^= 1
			write(t, dest, evil)
		},
		"a truncated library": func(t *testing.T, dest string) { write(t, dest, genuine[:10]) },
		"an empty file":       func(t *testing.T, dest string) { write(t, dest, nil) },
		"a longer library":    func(t *testing.T, dest string) { write(t, dest, append(bytes.Clone(genuine), "tail"...)) },
		"a link to another file": func(t *testing.T, dest string) {
			target := filepath.Join(t.TempDir(), "evil")
			write(t, target, []byte("evil"))
			link(t, target, dest)
		},
		"a link to the genuine bytes": func(t *testing.T, dest string) {
			// The bytes check out today; the link's target is someone else's to change.
			target := filepath.Join(t.TempDir(), "copy")
			write(t, target, genuine)
			link(t, target, dest)
		},
		"a dangling link": func(t *testing.T, dest string) { link(t, filepath.Join(t.TempDir(), "nothing"), dest) },
		"a directory": func(t *testing.T, dest string) {
			if err := os.MkdirAll(filepath.Join(dest, "inside"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"a read-only planted file": func(t *testing.T, dest string) {
			write(t, dest, []byte("evil"))
			if err := os.Chmod(dest, 0o400); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, do := range plant {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, cacheDir, libffiVersion, "libffi.test")
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			do(t, dest)
			path, err := verifiedLibrary(base, "libffi.test", genuine)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(path, base+string(filepath.Separator)) {
				// What was planted could not be replaced: a private directory, removed here.
				t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
			}
			loaded(t, path)
		})
	}
}

// TestAnUnusableCacheStillLoadsCheckedBytes: with the cache directory unwritable, missing, or
// a file where the directory should be, the library is written to a private directory of its
// own and loaded from there — never by its bare name.
func TestAnUnusableCacheStillLoadsCheckedBytes(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"no cache directory at all": func(*testing.T) string { return "" },
		"a file where the cache directory goes": func(t *testing.T) string {
			base := t.TempDir()
			write(t, filepath.Join(base, "github.com"), []byte("in the way"))
			return base
		},
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		cases["a cache directory that cannot be written"] = func(t *testing.T) string {
			base := t.TempDir()
			if err := os.Chmod(base, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
			return base
		}
		cases["a planted library in a directory that cannot be written"] = func(t *testing.T) string {
			base := t.TempDir()
			dir := filepath.Join(base, cacheDir, libffiVersion)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "libffi.test"), []byte("evil"))
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			return base
		}
	}
	for name, make := range cases {
		t.Run(name, func(t *testing.T) {
			base := make(t)
			path, err := verifiedLibrary(base, "libffi.test", genuine)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
			loaded(t, path)
			if base != "" && strings.HasPrefix(path, base+string(filepath.Separator)) {
				t.Errorf("loaded from %s, inside the cache that could not be used", path)
			}
			if info, err := os.Stat(filepath.Dir(path)); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
				t.Errorf("the private directory's mode: %v %v", info, err)
			}
		})
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}
