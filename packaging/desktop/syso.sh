#!/usr/bin/env bash
# Generates the desktop client's Windows resource objects from the kit's icon (REQ-PKG-005,
# delta `2026-10-wails-icons`): cmd/umbral-desktop/rsrc_windows_{amd64,arm64}.syso. Go links a
# `*_windows_<arch>.syso` into Windows builds only, so the Linux build is unaffected.
#
# go-winres is the pure-Go tool behind Wails' own `generate syso`, pinned here and run outside
# the module, so neither go.mod gains a dependency. `--check` regenerates into a temporary
# directory and fails if the committed files differ.
set -euo pipefail
cd "$(dirname "$0")/../.."

WINRES="github.com/tc-hib/go-winres@v0.3.3"
ICON="assets/branding/umbral-icons/windows/umbral.ico"
DEST="cmd/umbral-desktop"

out="$DEST"
if [ "${1:-}" = "--check" ]; then
  out="$(mktemp -d)"
  trap 'rm -rf "$out"' EXIT
fi

GOFLAGS= go run "$WINRES" simply --icon "$ICON" --arch amd64,arm64 --out "$out/rsrc"

if [ "${1:-}" = "--check" ]; then
  for arch in amd64 arm64; do
    cmp -s "$out/rsrc_windows_$arch.syso" "$DEST/rsrc_windows_$arch.syso" ||
      { echo "syso: $DEST/rsrc_windows_$arch.syso is not what $ICON generates; run packaging/desktop/syso.sh" >&2; exit 1; }
  done
  echo "syso: the committed resources match $ICON"
else
  ls -l "$DEST"/rsrc_windows_*.syso
fi
