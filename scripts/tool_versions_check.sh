#!/usr/bin/env bash
# Checks that what the gate runs is pinned, and pinned once:
#
#   - the gate's Go tools carry the same version in the two places that install them,
#     .github/workflows/ci.yml's env block and Taskfile.yml's tools:install;
#   - no workflow installs or runs a Go tool at a version written in the step itself, so the
#     env block is the only place a workflow's version lives;
#   - nothing installs or runs an unpinned `@latest`, and no action asks for `version: latest`;
#   - every action in every workflow is referenced by a 40-digit commit SHA.
#
# The pre-commit hooks run the tools `task tools:install` put on PATH, and CI installs its
# own; a version bumped in one file only makes the local gate and the pipeline disagree
# about what passes.
#
#   scripts/tool_versions_check.sh [ROOT]
#
# ROOT is the tree to check, the repository by default; scripts/tool_versions_selftest.sh
# passes mutated copies to prove each rule still rejects what it is there for.
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"

ci=.github/workflows/ci.yml
tf=Taskfile.yml
shopt -s nullglob
workflows=(.github/workflows/*.yml .github/workflows/*.yaml)
fail=0

check() { # <env name in ci.yml> <module path>
  local want got
  want="$(sed -nE "s/^  $1: (v[0-9][^ ]*)\$/\1/p" "$ci")"
  got="$(sed -nE "s#^ *- go install $2@(v[0-9][^ ]*)\$#\1#p" "$tf")"
  if [[ -z "$want" || -z "$got" ]]; then
    echo "tool versions: $1 is '${want:-missing}' in $ci and '${got:-missing}' in $tf" >&2
    fail=1
  elif [[ "$want" != "$got" ]]; then
    echo "tool versions: $1 is $want in $ci but $2 is $got in $tf" >&2
    fail=1
  fi
}

check GOFUMPT_VERSION mvdan.cc/gofumpt
check GOVULNCHECK_VERSION golang.org/x/vuln/cmd/govulncheck
check GOLANGCI_LINT_VERSION github.com/golangci/golangci-lint/v2/cmd/golangci-lint
check GO_ARCH_LINT_VERSION github.com/fe3dback/go-arch-lint

if [[ ${#workflows[@]} -eq 0 ]]; then
  echo "tool versions: no workflow under .github/workflows" >&2
  exit 1
fi

if grep -nE 'go (install|run) [^ ]+@latest' "${workflows[@]}" "$tf" >&2; then
  echo "tool versions: the lines above use an unpinned @latest" >&2
  fail=1
fi

if grep -nE 'go (install|run) [^ ]+@v[0-9]' "${workflows[@]}" >&2; then
  echo "tool versions: the lines above write a version in the step; use ci.yml's env block" >&2
  fail=1
fi

if grep -nE '^\s*version:\s*["'"'"']?latest' "${workflows[@]}" >&2; then
  echo "tool versions: the lines above ask an action for its latest version" >&2
  fail=1
fi

# A local action (./path) is the repository's own code and has no ref.
if grep -nE '^\s*(- )?uses:' "${workflows[@]}" | grep -vE 'uses:\s*\./' |
  grep -vE 'uses:\s*[^ @]+@[0-9a-f]{40}(\s|$)' >&2; then
  echo "tool versions: the actions above are not pinned by commit SHA" >&2
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "tool versions: ci.yml and Taskfile.yml agree, and every action is pinned"
