package shellinteg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sweep removes the bootstrap directories a previous daemon left in dir and reports how
// many it removed (REQ-TERM-012).
//
// The normal path already cleans up after itself: Bootstrap.Close removes the directory
// when the shell exits, and a clean shutdown leaks nothing — measured, on a real daemon
// with live sessions. What nothing can clean up is `kill -9`: the directory name is
// random and recorded nowhere, so without this sweep the residue is permanent and grows by
// one directory per live session per crash.
//
// Safety comes from *where* this is called, not from anything decided here. The caller
// holds the instance lock, so it is the only daemon of this installation and every
// bootstrap directory below dir belongs to a process that is gone; that is why there is no
// age heuristic and no ownership check. Sweeping the shared temporary directory instead
// could not make that argument, which is the reason Prepare stopped writing there.
//
// A directory that cannot be removed is reported rather than raised: the caller logs it and
// keeps starting. Litter is not a reason to refuse to serve.
func Sweep(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A runtime directory that does not exist yet is the first start on this machine,
		// which has nothing to sweep.
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("shellinteg: read %s: %w", dir, err)
	}

	var (
		removed int
		failed  []error
	)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), dirPrefix) {
			continue
		}
		// One failure must not hide the rest: the socket, the token and the lock live in
		// this same directory, and stopping early would leave orphans behind the one
		// directory whose permissions someone changed.
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			failed = append(failed, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(failed...)
}
