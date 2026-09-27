#!/usr/bin/env bash
# Builds libghostty-vt, which the sessions emulator links through cgo.
#
# The Go bindings (go.mitchellh.com/libghostty) are a cgo package that links
# libghostty-vt statically through pkg-config. The library itself is written in Zig, so
# building it needs Zig 0.16.0 or newer on PATH; nothing else in this repository does.
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
PC="$PREFIX/share/pkgconfig/libghostty-vt-static.pc"

if [[ -f "$PC" ]]; then
  echo "libghostty-vt: already built at $PREFIX"
  exit 0
fi

if ! command -v zig >/dev/null 2>&1; then
  echo "libghostty-vt: zig is not on PATH. Install Zig 0.16.0 or newer:" >&2
  echo "  snap install zig --classic --beta    # or see https://ziglang.org/download/" >&2
  exit 1
fi

# Ghostty's repository is the source of libghostty-vt; there is no separate distribution.
src="$(mktemp -d)"
trap 'rm -rf "$src"' EXIT

echo "libghostty-vt: cloning ghostty"
git clone --depth 1 https://github.com/ghostty-org/ghostty "$src/ghostty" >/dev/null 2>&1

echo "libghostty-vt: building with $(zig version) into $PREFIX"
( cd "$src/ghostty" && zig build -Demit-lib-vt --prefix "$PREFIX" )

if [[ ! -f "$PC" ]]; then
  echo "libghostty-vt: the build finished but $PC is missing" >&2
  exit 1
fi

echo "libghostty-vt: built. Add this to your shell if you build outside Task:"
echo "  export PKG_CONFIG_PATH=\"$PREFIX/share/pkgconfig\""
