// Umbral: always embedded (T-HRD-18); the build constraint is the file name.

package ffi

import _ "embed"

const libname = "libffi-8.dll"

//go:embed assets/libffi/windows_arm64/libffi-8.dll
var embeddedLib []byte
