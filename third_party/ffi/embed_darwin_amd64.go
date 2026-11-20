// Umbral: always embedded (T-HRD-18); the build constraint is the file name.

package ffi

import _ "embed"

const libname = "libffi.8.dylib"

//go:embed assets/libffi/darwin_amd64/libffi.8.dylib
var embeddedLib []byte
