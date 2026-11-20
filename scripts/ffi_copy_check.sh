#!/usr/bin/env bash
# third_party/ffi is upstream's module with a handful of files changed (T-HRD-18). This
# proves the rest — the four embedded libraries above all — is upstream's, byte for byte:
# a binary nobody can read in a diff must not be the place a change hides.
set -euo pipefail
cd "$(dirname "$0")/.."

copy=third_party/ffi
version=$(cat "$copy/UPSTREAM_VERSION")
# Downloaded and verified against the checksum database, outside this repository's go.mod,
# whose replace would otherwise answer with the copy itself.
upstream=$(cd "$(mktemp -d)" && go mod init probe >/dev/null 2>&1 && go mod download -json "github.com/jupiterrider/ffi@$version" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",\{0,1\}$/\1/p')
[ -d "$upstream" ] || { echo "ffi_copy_check: could not download github.com/jupiterrider/ffi@$version" >&2; exit 1; }

ours=" verified.go verified_test.go embed_test.go embed_other.go UMBRAL.md UPSTREAM_VERSION "
changed=" embed.go embed_darwin_amd64.go embed_darwin_arm64.go embed_windows_amd64.go embed_windows_arm64.go "
left_out_re='^(examples/|example_test\.go$)'
fail=0

while IFS= read -r f; do
  case "$ours$changed" in *" $f "*) continue ;; esac
  if [ ! -f "$upstream/$f" ]; then
    echo "ffi_copy_check: $copy/$f is not in upstream $version and is not listed as Umbral's" >&2; fail=1
  elif ! cmp -s "$copy/$f" "$upstream/$f"; then
    echo "ffi_copy_check: $copy/$f differs from upstream $version" >&2; fail=1
  fi
done < <(cd "$copy" && find . -type f | sed 's#^\./##' | sort)

while IFS= read -r f; do
  [[ "$f" =~ $left_out_re ]] && continue
  [ -f "$copy/$f" ] || { echo "ffi_copy_check: upstream's $f is missing from $copy" >&2; fail=1; }
done < <(cd "$upstream" && find . -type f | sed 's#^\./##' | sort)

for f in $ours $changed; do
  [ -f "$copy/$f" ] || { echo "ffi_copy_check: $copy/$f, listed as changed, is missing" >&2; fail=1; }
done

[ "$fail" = 0 ] || exit 1
echo "ffi_copy_check: OK — $copy is upstream $version but for the files UMBRAL.md lists"
