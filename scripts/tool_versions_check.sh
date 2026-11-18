#!/usr/bin/env bash
# Checks that the gate's Go tools are pinned to the same versions in the two places that
# install them: .github/workflows/ci.yml's env block and Taskfile.yml's tools:install.
#
# The pre-commit hooks run the tools `task tools:install` put on PATH, and CI installs its
# own; a version bumped in one file only makes the local gate and the pipeline disagree
# about what passes. It also fails on an unpinned `@latest`.
#
#   scripts/tool_versions_check.sh
set -euo pipefail
cd "$(dirname "$0")/.."

ci=.github/workflows/ci.yml
tf=Taskfile.yml
fail=0

check() { # <env name in ci.yml> <module path in Taskfile.yml>
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

if grep -nE 'go install [^ ]+@latest' "$ci" .github/workflows/perf.yml "$tf" >&2; then
  echo "tool versions: the lines above install an unpinned @latest" >&2
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then exit 1; fi
echo "tool versions: ci.yml and Taskfile.yml agree"
