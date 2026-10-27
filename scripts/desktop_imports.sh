#!/usr/bin/env bash
# The desktop window's boundary (delta `2026-10-desktop-client`, decision 1). cmd/umbral-desktop
# is a module of its own, which go-arch-lint excludes, so this is its rule: of Umbral's own
# packages it imports the client, the bridge and the bridge's frame renderer, and nothing else
# — everything it does goes through the bridge and the published protocol (REQ-DSK-001).
#
# With --selftest it adds a forbidden import to a copy of the module, then breaks its go.mod,
# and asserts both are refused and the clean copy is not.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALLOWED=(
  github.com/ecrespo/umbral/internal/client
  github.com/ecrespo/umbral/internal/desktop
  github.com/ecrespo/umbral/internal/desktop/adapters/vtframe
)

check() {
  local dir=$1 bad=0 imports
  # Every package of the module and its tests; a module that cannot be listed fails the gate.
  if ! imports="$(cd "$dir" && go list -test -f '{{range .Imports}}{{println .}}{{end}}{{range .TestImports}}{{println .}}{{end}}' ./...)"; then
    echo "desktop_imports: cannot list cmd/umbral-desktop's packages" >&2
    return 1
  fi
  while read -r imp; do
    case "$imp" in github.com/ecrespo/umbral/*) ;; *) continue ;; esac
    local ok=0
    for a in "${ALLOWED[@]}"; do [ "$imp" = "$a" ] && ok=1; done
    if [ $ok = 0 ]; then echo "desktop_imports: cmd/umbral-desktop imports $imp" >&2; bad=1; fi
  done <<< "$imports"
  return $bad
}

if [ "${1:-}" = "--selftest" ]; then
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  cp -r "$ROOT/cmd/umbral-desktop" "$WORK/m"
  # The copy's replace must still name this repository.
  (cd "$WORK/m" && go mod edit -replace "github.com/ecrespo/umbral=$ROOT")
  cat > "$WORK/m/forbidden.go" <<'GO'
package main

import _ "github.com/ecrespo/umbral/internal/store"
GO
  if check "$WORK/m" 2>/dev/null; then
    echo "desktop_imports: selftest FAIL — an import of internal/store was accepted" >&2
    exit 1
  fi
  rm "$WORK/m/forbidden.go"
  check "$WORK/m" >/dev/null || { echo "desktop_imports: selftest FAIL — the clean copy was refused" >&2; exit 1; }
  # A module the gate cannot read is refused, never passed.
  echo "this is not a go.mod" > "$WORK/m/go.mod"
  if check "$WORK/m" 2>/dev/null; then
    echo "desktop_imports: selftest FAIL — an unreadable module was accepted" >&2
    exit 1
  fi
  echo "desktop_imports: selftest ok — a forbidden import and an unreadable module are refused"
  exit 0
fi

check "$ROOT/cmd/umbral-desktop"
echo "desktop_imports: cmd/umbral-desktop imports only the client and the bridge"
