package skills

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ecrespo/umbral/internal/context/domain"
)

// maxTarStream bounds what a tarball may decompress to, headers and padding included, on top
// of the per-file and per-bundle limits the builder applies to contents.
const maxTarStream = domain.MaxSkillBundleBytes + domain.MaxSkillEntries*4096

// ReadBundle checks the bundle at path, a directory or a `.tar.gz`, and holds it in memory.
// The path is classified as local before anything resolves it (decision 4), and must be
// absolute: `umb` resolves it, and the daemon's own working directory means nothing.
func ReadBundle(path string) (domain.Bundle, error) {
	if domain.IsRemoteSource(path) {
		return domain.Bundle{}, fmt.Errorf("%w: %q is not a local path; skills install from a directory or .tar.gz on this machine only, until signed skill bundles exist (Art. 5)",
			domain.ErrSkillValidation, path)
	}
	if !filepath.IsAbs(path) {
		return domain.Bundle{}, fmt.Errorf("%w: %q must be an absolute path", domain.ErrSkillValidation, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("%w: %s: %w", domain.ErrSkillValidation, path, err)
	}
	switch {
	case info.IsDir():
		return readDir(path)
	case info.Mode().IsRegular() && (strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".tgz")):
		return readTarball(path)
	default:
		return domain.Bundle{}, fmt.Errorf("%w: %s is neither a directory nor a .tar.gz", domain.ErrSkillValidation, path)
	}
}

func readDir(root string) (domain.Bundle, error) {
	b := domain.NewBundleBuilder()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: %s: %w", domain.ErrSkillValidation, p, err)
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", domain.ErrSkillValidation, p, err)
		}
		rel = filepath.ToSlash(rel)
		switch t := d.Type(); {
		case t.IsDir():
			return b.AddDir(rel)
		case t.IsRegular():
			return addRegular(b, p, rel)
		default:
			return fmt.Errorf("%w: entry %q: only regular files and directories are allowed, not %v", domain.ErrSkillValidation, rel, t)
		}
	})
	if err != nil {
		return domain.Bundle{}, err
	}
	return b.Finish()
}

// addRegular reads one file the walk saw as regular, refusing a hardlink and a file swapped
// for something else between the walk and the open.
func addRegular(b *domain.BundleBuilder, p, rel string) error {
	before, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("%w: entry %q: %w", domain.ErrSkillValidation, rel, err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("%w: entry %q: changed into something other than a regular file", domain.ErrSkillValidation, rel)
	}
	if hasOtherNames(before) {
		return fmt.Errorf("%w: entry %q: a hardlink", domain.ErrSkillValidation, rel)
	}
	f, err := openEntry(p)
	if err != nil {
		return fmt.Errorf("%w: entry %q: %w", domain.ErrSkillValidation, rel, err)
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("%w: entry %q: changed while it was read", domain.ErrSkillValidation, rel)
	}
	return b.AddFile(rel, f)
}

func readTarball(path string) (domain.Bundle, error) {
	f, err := os.Open(path) // #nosec G304 -- the tarball the user asked to inspect
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("%w: %s: %w", domain.ErrSkillValidation, path, err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("%w: %s is not gzip: %w", domain.ErrSkillValidation, path, err)
	}
	defer func() { _ = gz.Close() }()
	stream := &io.LimitedReader{R: gz, N: maxTarStream}
	tr := tar.NewReader(stream)
	b := domain.NewBundleBuilder()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return domain.Bundle{}, fmt.Errorf("%w: %s: %w", domain.ErrSkillValidation, path, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = b.AddDir(hdr.Name)
		case tar.TypeReg:
			err = b.AddFile(hdr.Name, tr)
		default:
			err = fmt.Errorf("%w: entry %q: only regular files and directories are allowed", domain.ErrSkillValidation, hdr.Name)
		}
		if err != nil {
			return domain.Bundle{}, err
		}
	}
	// A cut on a block boundary reads as a clean end: a stream that reached its bound is
	// refused whole instead of read as the entries before the cut.
	if stream.N <= 0 {
		return domain.Bundle{}, fmt.Errorf("%w: %s decompresses past %d bytes of tar stream", domain.ErrSkillValidation, path, maxTarStream)
	}
	return b.Finish()
}
