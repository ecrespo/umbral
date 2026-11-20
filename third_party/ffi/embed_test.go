//go:build darwin || windows

package ffi

import (
	"path/filepath"
	"testing"
)

// TestTheLibraryLoadedIsTheEmbeddedOne is Umbral's (T-HRD-18): by the time a test runs, init
// has loaded libffi. It was loaded from an absolute path whose file holds the embedded bytes,
// whatever `FFI_NO_EMBED` says — CI runs this with the variable set, which upstream answers
// by loading the library by its bare name.
func TestTheLibraryLoadedIsTheEmbeddedOne(t *testing.T) {
	if !filepath.IsAbs(filename) {
		t.Fatalf("libffi was loaded as %q, a name the system resolves along a search path", filename)
	}
	if !holds(filename, embeddedLib) {
		t.Fatalf("%s does not hold the embedded libffi", filename)
	}
	if got := filepath.Base(filename); got != libname {
		t.Errorf("loaded %s, want a file named %s", got, libname)
	}
}
