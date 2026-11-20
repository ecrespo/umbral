//go:build !darwin && !windows

package ffi

// libffiVersion names the cache directory in verified.go, which only macOS and Windows use:
// elsewhere the system's own libffi is loaded.
const libffiVersion = "3.8.0"
