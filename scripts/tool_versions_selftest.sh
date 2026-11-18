#!/usr/bin/env bash
# Proves scripts/tool_versions_check.sh still rejects what it is there for.
#
# A check that passes on a clean tree says nothing about whether it would fail on a dirty
# one. Each case copies the workflows and the Taskfile, breaks one rule in the copy and
# asserts the check refuses it; the untouched copy must pass. Nothing in the repository is
# modified, and the copies are removed on exit.
#
#   scripts/tool_versions_selftest.sh
set -euo pipefail
cd "$(dirname "$0")/.."

check="$PWD/scripts/tool_versions_check.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fresh() { # a clean copy of what the check reads, printed as its path
  local root="$tmp/$1"
  mkdir -p "$root/.github/workflows"
  cp .github/workflows/*.yml "$root/.github/workflows/"
  cp Taskfile.yml "$root/"
  echo "$root"
}

fail=0
rejects() { # <case name> <file, relative> <sed expression>, or <case name> + <file> <line to append>
  local name="$1" root
  root="$(fresh "$name")"
  if [[ "$2" == + ]]; then
    printf '%s\n' "$4" >>"$root/$3"
  else
    sed -i.bak -E "$3" "$root/$2" && rm -f "$root/$2.bak"
    if cmp -s "$2" "$root/$2"; then
      echo "selftest: case '$name' changed nothing; its pattern is stale" >&2
      fail=1
      return
    fi
  fi
  if "$check" "$root" >/dev/null 2>&1; then
    echo "selftest: '$name' was accepted" >&2
    fail=1
  fi
}

ci=.github/workflows/ci.yml

if ! "$check" "$(fresh clean)" >/dev/null; then
  echo "selftest: the untouched copy is rejected" >&2
  exit 1
fi

rejects "a version bumped in ci.yml only" "$ci" 's/^(  GO_ARCH_LINT_VERSION: ).*/\1v9.9.9/'
rejects "a version bumped in the Taskfile only" Taskfile.yml 's#(go install mvdan.cc/gofumpt@).*#\1v9.9.9#'
rejects "an env version removed" "$ci" '/^  GOVULNCHECK_VERSION: /d'
rejects "@latest back in the Taskfile" Taskfile.yml 's#(go install github.com/fe3dback/go-arch-lint@).*#\1latest#'
rejects "@latest back in a step" "$ci" 's#(go install github.com/fe3dback/go-arch-lint@).*#\1latest#'
rejects "a version written in a step" "$ci" 's#(go install github.com/fe3dback/go-arch-lint@).*#\1v9.9.9#'
rejects "go run @latest in perf.yml" + .github/workflows/perf.yml '      - run: go run example.com/tool@latest'
rejects "an action's version: latest" "$ci" 's/^( +version: )\$\{\{ env\.GOLANGCI_LINT_VERSION \}\}/\1latest/'
rejects "an action back on a tag" "$ci" 's#(uses: actions/checkout@)[0-9a-f]{40}.*#\1v5#'
rejects "a short SHA" "$ci" 's#(uses: actions/checkout@)([0-9a-f]{12})[0-9a-f]{28}#\1\2#'
rejects "an unpinned action in a new workflow" + .github/workflows/new.yml '      - uses: actions/checkout@v5'
rejects "@latest in a new workflow" + .github/workflows/new.yml '      - run: go install example.com/tool@latest'

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "tool versions selftest: 12 mutations rejected, the clean tree accepted"
