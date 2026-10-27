#!/usr/bin/env bash
# Builds bin/umbral-desktop in the container of this directory's Dockerfile, with the host's Go
# toolchain, module cache and libghostty-vt mounted at their own paths (the .pc file names an
# absolute prefix). Run by `task desktop`. VERSION sets main.version.
set -euo pipefail
cd "$(dirname "$0")/../.."

ghostty="${GHOSTTY_PREFIX:-$HOME/.local/ghostty-vt}"
[ -f "$ghostty/share/pkgconfig/libghostty-vt.pc" ] || { echo "desktop: run \`task deps:ghostty\` first" >&2; exit 1; }
goroot="$(go env GOROOT)"
modcache="$(go env GOMODCACHE)"
version="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 0.0.0-dev)}"

docker build -q -t umbral-desktop-build packaging/desktop >/dev/null
mkdir -p bin
docker run --rm \
  -v "$goroot:/goroot:ro" -v "$modcache:/gomod" -v "$ghostty:$ghostty:ro" -v "$PWD:/src" \
  -w /src/cmd/umbral-desktop \
  -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=local -e HOME=/tmp \
  -e PKG_CONFIG_PATH="$ghostty/share/pkgconfig" \
  -u "$(id -u):$(id -g)" umbral-desktop-build \
  sh -c "/goroot/bin/go vet ./... && /goroot/bin/go build -ldflags '-s -w -X main.version=$version' -o /src/bin/umbral-desktop ."
echo "desktop: bin/umbral-desktop"
