//go:build darwin || windows

package ffi

import (
	_ "embed"
	"os"
)

const libffiVersion = "3.8.0"

//go:embed assets/libffi/LICENSE
var embeddedLicense []byte

// init names the library to load: the embedded one, checked (verified.go). Upstream's
// `FFI_NO_EMBED` and its `ffi_no_embed` build tag are gone from this copy: both ended in a
// load by bare name.
func init() {
	base, err := os.UserCacheDir()
	if err != nil {
		base = ""
	}
	path, err := verifiedLibrary(base, libname, embeddedLib)
	if err != nil {
		panic(err)
	}
	filename = path
}
