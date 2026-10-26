#!/usr/bin/env bash
# Installs llama.cpp's shared libraries, which the embedded yzma provider loads in process
# (REQ-LLM-009, delta `2026-10-yzma-provider`). yzma v1.30 needs release b11480 or later; this
# pins b11899 and checks the archive's SHA-256 before extracting it.
#
# Idempotent: if the directory already holds libllama, it does nothing.
#
#   scripts/install_llama.sh [prefix]     default prefix: ~/.local/llama-cpp
#
# The libraries land in <prefix>/b11899/llama-b11899: that directory is a yzma provider's
# lib_path in models.toml.
set -euo pipefail

PREFIX="${1:-$HOME/.local/llama-cpp}"
RELEASE="b11899"
DEST="$PREFIX/$RELEASE"
LIBDIR="$DEST/llama-$RELEASE"

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) ASSET="ubuntu-x64"; SHA="464e0dfdd70d18a3b758074ac58e04fa27f892a771805dd883b2d0f3c5ca0289"; LIB="libllama.so" ;;
  Darwin-arm64) ASSET="macos-arm64"; SHA="8cc732b23afa601fa2deff6ceb60f938bae4559171af4fde0e68132be6332bf2"; LIB="libllama.dylib" ;;
  Darwin-x86_64) ASSET="macos-x64"; SHA="b5f9d1bda68b1529a8fb4aae719f8e97507d7d122501c89db834df6d1d8314a1"; LIB="libllama.dylib" ;;
  *) echo "llama.cpp: no pinned $RELEASE build for $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

if [[ -e "$LIBDIR/$LIB" ]]; then
  echo "llama.cpp: $RELEASE already installed at $LIBDIR"
  exit 0
fi

NAME="llama-$RELEASE-bin-$ASSET.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsSL -o "$TMP/$NAME" "https://github.com/ggml-org/llama.cpp/releases/download/$RELEASE/$NAME"
if command -v sha256sum >/dev/null; then
  GOT="$(sha256sum "$TMP/$NAME" | cut -d' ' -f1)"
else
  GOT="$(shasum -a 256 "$TMP/$NAME" | cut -d' ' -f1)"
fi
if [[ "$GOT" != "$SHA" ]]; then
  echo "llama.cpp: $NAME has SHA-256 $GOT, want $SHA" >&2
  exit 1
fi
mkdir -p "$DEST"
tar -xzf "$TMP/$NAME" -C "$DEST"
if [[ ! -e "$LIBDIR/$LIB" ]]; then
  echo "llama.cpp: $NAME did not hold $LIB under llama-$RELEASE/" >&2
  exit 1
fi
echo "llama.cpp: $RELEASE installed; lib_path = \"$LIBDIR\""
