#!/usr/bin/env bash
# Builds libghostty-vt, which the sessions emulator links through cgo.
#
# The Go bindings (go.mitchellh.com/libghostty) are a cgo package that links
# libghostty-vt statically through pkg-config. The library itself is written in Zig, so
# building it needs Zig 0.16.x on PATH (the pinned ghostty does not build with 0.17); nothing else in this repository does.
#
# Idempotent: if the prefix already carries a usable pkg-config file, it does nothing.
#
#   scripts/build_libghostty.sh [prefix]     default prefix: ~/.local/ghostty-vt
#
# Afterwards, export the path pkg-config needs:
#
#   export PKG_CONFIG_PATH="$HOME/.local/ghostty-vt/share/pkgconfig"
#
# The Taskfile adds the default prefix automatically, so `task test` works without it.
set -euo pipefail

PREFIX="${1:-$HOME/.local/ghostty-vt}"

# The ghostty commit whose C headers match the pinned Go bindings in go.mod. Ghostty's main
# moves under them: on 2026-10-17 it no longer declared GHOSTTY_OSC_COMMAND_CONTEXT_SIGNAL,
# which go.mitchellh.com/libghostty v0.0.0-20260908040635 uses, and every CI build failed.
# Bump this together with the bindings, never alone.
GHOSTTY_REF="d00a498274f321b8808ba89d63c4e23d93a7d8e0"
PC="$PREFIX/share/pkgconfig/libghostty-vt-static.pc"

if [[ -f "$PC" ]]; then
  echo "libghostty-vt: already built at $PREFIX"
  exit 0
fi

if ! command -v zig >/dev/null 2>&1; then
  echo "libghostty-vt: zig is not on PATH. Install Zig 0.16.x:" >&2
  echo "  snap install zig --classic --beta    # or see https://ziglang.org/download/" >&2
  exit 1
fi

# Ghostty's repository is the source of libghostty-vt; there is no separate distribution.
src="$(mktemp -d)"
trap 'rm -rf "$src"' EXIT

echo "libghostty-vt: fetching ghostty at ${GHOSTTY_REF:0:12}"
git init -q "$src/ghostty"
git -C "$src/ghostty" fetch -q --depth 1 https://github.com/ghostty-org/ghostty "$GHOSTTY_REF"
git -C "$src/ghostty" checkout -q FETCH_HEAD

echo "libghostty-vt: building with $(zig version) into $PREFIX"
( cd "$src/ghostty" && zig build -Demit-lib-vt --prefix "$PREFIX" )

if [[ ! -f "$PC" ]]; then
  echo "libghostty-vt: the build finished but $PC is missing" >&2
  exit 1
fi

echo "libghostty-vt: built. Add this to your shell if you build outside Task:"
echo "  export PKG_CONFIG_PATH=\"$PREFIX/share/pkgconfig\""
