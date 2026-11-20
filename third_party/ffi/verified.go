package ffi

// This file is Umbral's, not upstream's (T-HRD-18, delta `2026-11-libffi-load`).
//
// Upstream writes the embedded libffi to the user's cache directory once and afterwards loads
// whatever file is at that path. Any process of the same user can put a file there, and what
// it puts runs inside the program at its next start. Here the file at the cache path is
// loaded only when it is a regular file holding exactly the embedded bytes; anything else is
// replaced by them, and where the cache cannot be used the bytes go to a private directory.
// The library is never loaded by a bare name, which the system would resolve along a search
// path.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// cacheDir is where the library is kept under the user's cache directory, as upstream has it.
const cacheDir = "github.com/jupiterrider/ffi/libffi"

// verifiedLibrary returns the path of a regular file that holds exactly lib, to be loaded.
// base is the user's cache directory, "" when there is none.
func verifiedLibrary(base, name string, lib []byte) (string, error) {
	if len(lib) == 0 {
		return "", errors.New("ffi: no embedded libffi to load")
	}
	var cached error
	if base != "" {
		dest := filepath.Join(base, cacheDir, libffiVersion, name)
		if holds(dest, lib) {
			return dest, nil
		}
		if cached = place(dest, lib); cached == nil && holds(dest, lib) {
			return dest, nil
		}
	}
	// The cache cannot be used: a directory of this process's own, which nobody else was
	// told the name of.
	dir, err := os.MkdirTemp("", "umbral-libffi-*")
	if err != nil {
		return "", fmt.Errorf("ffi: no place to write libffi (cache: %v): %w", cached, err)
	}
	dest := filepath.Join(dir, name)
	if err := place(dest, lib); err != nil || !holds(dest, lib) {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("ffi: libffi could not be written to %s: %v", dir, err)
	}
	return dest, nil
}

// holds reports whether path is a regular file — not a link to one — with exactly want.
func holds(path string, want []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(want)) {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// What was opened is what was looked at: a file swapped in between is another file.
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false
	}
	got, err := io.ReadAll(io.LimitReader(f, int64(len(want))+1))
	return err == nil && bytes.Equal(got, want)
}

// place writes lib at dest through a new file of its own name, moved over whatever is there:
// a planted file or link is replaced, never written through.
func place(dest string, lib []byte) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".libffi-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(lib)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return errors.Join(werr, cerr)
	}
	// A directory in the way cannot be renamed over; it is not this module's to empty.
	if err := os.Rename(name, dest); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
