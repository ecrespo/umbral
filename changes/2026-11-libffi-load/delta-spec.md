# Delta — the daemon loads only a libffi it has checked (macOS, Windows)

| Field | Value |
|---|---|
| **Status** | `PROPOSED`. Written with its code: the Tech Lead asked for T-HRD-18 on 2026-11-19 in one instruction with the tasks around it. It is the Tech Lead's to ratify |
| **Date** | 2026-11-19 |
| **Task** | T-HRD-18 |
| **REQ** | none new; Art. 5 |
| **Raised by** | Delta `2026-11-open-questions`, L2 and L3 |

## Evidence

`github.com/jupiterrider/ffi` v0.8.0, which `yzma`'s binding imports and `umbrald` therefore
links, loads libffi in a package `init`, before any line of Umbral's runs:

- On macOS and Windows x86_64 `embed.go` writes the embedded library to
  `<user cache>/github.com/jupiterrider/ffi/libffi/3.8.0/` **if no file is there**, and loads
  whatever is. A file, a link or a truncated library left at that path by any process of the
  same user is loaded as it is, and its code runs inside the daemon at every start.
- With `FFI_NO_EMBED=1` in the environment, with the `ffi_no_embed` build tag, or when the
  cache directory cannot be made, `init.go` loads the library by its bare name, which the
  system resolves along a search path — on Windows the working directory among it.
- None of this can be changed from Umbral's code: a dependency's `init` runs first.

## Decisions

1. **A copy of the module, with one change, through a `replace`.** `third_party/ffi` is
   upstream v0.8.0 minus its examples; `go.mod` replaces the module with it. Changed:
   `embed.go`, whose `init` now calls `verifiedLibrary`, and the four `embed_<os>_<arch>.go`
   files, which lose the `ffi_no_embed` constraint. Added: `verified.go` and its tests.
2. **What is loaded.** The file at the cache path, when it is a regular file — not a link —
   holding exactly the bytes embedded in the binary. Otherwise the embedded bytes are written
   to a new file beside it and moved over what is there, and that is loaded after the same
   check. Where the cache cannot be used at all — no cache directory, one that cannot be
   written, a directory at the library's own path — they are written to a directory of the
   process's own under the temporary directory, mode 0700. If that fails too the daemon
   panics, as it does today without the library.
3. **Never a bare name on macOS or Windows.** `FFI_NO_EMBED` is ignored and the build tag is
   gone from the copy. Linux is unchanged: it loads the system's `libffi.so.8`, which only an
   administrator can replace.
4. **The copy is provably upstream's but for those files.** `scripts/ffi_copy_check.sh`
   downloads upstream at `third_party/ffi/UPSTREAM_VERSION` through the checksum database and
   compares every other file byte for byte, the four libraries included; a file that
   differs, is missing or is new fails `task ci` and CI's lint job. A binary cannot be read
   in a diff, so this is what keeps a change from hiding in one.
5. **Windows on arm64** is embedded too, where upstream loaded by bare name. It is not a
   target of the port (REQ-WIN-001) and nothing tests it.

## What it does not close

- **The moment between the check and the load.** The library is loaded by path; a process of
  the same user that replaces the file after it was checked and before it is opened wins.
  It has to be running at that instant, where before it could plant the file at any time
  and wait. Closing it needs a load from an open file, which neither system offers.
- **A process of the same user that is not confined** can change the daemon in other ways —
  its binary, its configuration, a shell's start-up file. This task closes the one that was
  specific to Umbral; the sandbox (T-HRD-15, T-HRD-16, T-HRD-17) is what bounds the rest, and
  its profile keeps the cache directory out of every write list.
- Moving to a newer upstream is manual: copy, re-apply decision 1, update the version.
  Dependabot no longer proposes it.

## Constitution check

- **Art. 5:** closes a way out of a `full-auto` worktree that outlived the thread.
- **Art. 9:** no new dependency; the Stack table names `yzma`, whose transitive dependency
  this is. A patched copy of a dependency is new in this repository, hence this delta.

## Spec changes

- Tech 1.100 §9.3: where each system's libffi comes from.

## Verification

- `third_party/ffi/verified_test.go`, on every system: a clean cache gets the library and
  the second start writes nothing; another library, one byte off, truncated, empty, longer,
  a link to another file, a link to the genuine bytes, a dangling link, a directory and a
  read-only file planted at the path are each never what is loaded; with no cache directory,
  an unwritable one, a planted library in an unwritable directory or a file in the way, the
  library comes from a private directory. Mutations: trusting an existing file, comparing
  lengths only, dropping both link checks, and writing the private copy inside the cache
  each turned a test red. Two checks have no test of their own: the size comparison, which
  is a shortcut before the byte comparison, and the re-check after writing.
- `embed_test.go`, on macOS and Windows only, with `FFI_NO_EMBED=1`: the library the test
  process itself loaded is at an absolute path and holds the embedded bytes. Its first run
  is this task's CI.
- `ffi_copy_check.sh` was run against a source file and a library with one byte appended;
  it refused both.
- Not verified: the daemon itself starting on macOS and Windows with a planted file in the
  runner's real cache directory — the tests plant in a directory of their own.
