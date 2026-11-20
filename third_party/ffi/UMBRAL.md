# Umbral's copy of `github.com/jupiterrider/ffi`

This directory is `github.com/jupiterrider/ffi` v0.8.0 (MIT, `LICENSE`), used through a
`replace` in the repository's `go.mod`, with one change (T-HRD-18, delta
`2026-11-libffi-load`): on macOS and Windows it loads only a libffi whose bytes it has
checked against the ones embedded in the binary, and never by a bare name.

| File | State |
|---|---|
| `verified.go`, `verified_test.go`, `embed_test.go`, `embed_other.go`, `UMBRAL.md` | Umbral's |
| `embed.go` | rewritten: `init` calls `verifiedLibrary`; `FFI_NO_EMBED` and the `ffi_no_embed` tag are gone |
| `embed_darwin_*.go`, `embed_windows_*.go` | the `ffi_no_embed` build constraint removed |
| `examples/`, `example_test.go` | not copied |
| everything else, the four libraries under `assets/` included | upstream's, byte for byte |

`scripts/ffi_copy_check.sh` (`task ffi:check`, in `task ci` and CI) downloads upstream's
module at the version in `third_party/ffi/UPSTREAM_VERSION` and fails if any file of the last
row differs, is missing, or is new. To move to a newer upstream: copy it over, re-apply the
rows above, update `UPSTREAM_VERSION`.
