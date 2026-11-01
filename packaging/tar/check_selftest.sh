#!/usr/bin/env bash
# Proves packaging/tar/check.sh rejects a broken archive (T-PKG-07).
#
#   packaging/tar/check_selftest.sh umbral-<version>-<os>-<arch>.tar.gz
#
# A checker that passes everything passes silently, so each case repacks the good archive with
# one defect and asserts check.sh fails on it. An archive of the other OS is made from the good
# one's binaries, which check.sh does not run there, so both OS paths are proved on either.
set -euo pipefail

archive="${1:?usage: check_selftest.sh ARCHIVE.tar.gz}"
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
ID="io.github.ecrespo.Umbral"
LABEL="io.github.ecrespo.umbral.umbrald"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

"$here/check.sh" "$archive" >/dev/null || { echo "tar selftest: the good archive fails" >&2; exit 1; }

top="$(basename "$archive" .tar.gz)"
os="$(sed -E 's/^umbral-.+-(linux|darwin)-(amd64|arm64)$/\1/' <<<"$top")"
other="$( [[ "$os" == linux ]] && echo darwin || echo linux )"
other_top="${top/-$os-/-$other-}"

# The other OS's archive, as build.sh would lay it out, from these binaries.
mkdir -p "$work/other/$other_top"
tar -xzf "$archive" -C "$work"
cp -R "$work/$top/bin" "$work/$top/LICENSE" "$work/other/$other_top/"
if [[ "$other" == linux ]]; then
  mkdir -p "$work/other/$other_top/share"
  ( cd "$root/assets/branding/umbral-icons/linux/share" && find applications icons/hicolor -type f ) |
    while IFS= read -r f; do mkdir -p "$work/other/$other_top/share/$(dirname "$f")"; cp "$root/assets/branding/umbral-icons/linux/share/$f" "$work/other/$other_top/share/$f"; done
else
  mkdir -p "$work/other/$other_top/share/launchd"
  cp "$root/packaging/launchd/$LABEL.plist" "$work/other/$other_top/share/launchd/"
fi
tar -C "$work/other" -czf "$work/$other_top.tar.gz" "$other_top"
"$here/check.sh" "$work/$other_top.tar.gz" >/dev/null || { echo "tar selftest: the good $other archive fails" >&2; exit 1; }

failed=0
mutant() {
  local base="$1" name="$2" edit="$3" t
  t="$(basename "$base" .tar.gz)"
  rm -rf "$work/m" && mkdir -p "$work/m/out" && tar -xzf "$base" -C "$work/m"
  ( cd "$work/m/$t" && eval "$edit" )
  tar -C "$work/m" -czf "$work/m/out/$t.tar.gz" "$t"
  if "$here/check.sh" "$work/m/out/$t.tar.gz" >/dev/null 2>&1; then
    echo "tar selftest: check.sh accepted an archive with $name" >&2
    failed=1
  else
    echo "tar selftest: rejected $name"
  fi
}

linux="$archive" darwin="$work/$other_top.tar.gz"
[[ "$os" == darwin ]] && { linux="$work/$other_top.tar.gz"; darwin="$archive"; }

mutant "$linux" "umbrald missing" "rm bin/umbrald"
mutant "$linux" "umb not executable" "chmod -x bin/umb"
mutant "$linux" "the 64 px icon missing" "rm share/icons/hicolor/64x64/apps/$ID.png"
mutant "$linux" "a .desktop not the kit's" "echo '# edited' >> share/applications/$ID.desktop"
mutant "$linux" "a launchd plist on linux" "mkdir -p share/launchd && cp '$root/packaging/launchd/$LABEL.plist' share/launchd/"
mutant "$darwin" "no launchd plist" "rm share/launchd/$LABEL.plist"
mutant "$darwin" "a plist running another path" "sed -i.bak 's|/usr/local/bin/umbrald|/opt/umbrald|' share/launchd/$LABEL.plist && rm share/launchd/$LABEL.plist.bak"
mutant "$darwin" "no LICENSE" "rm LICENSE"

[[ $failed -eq 0 ]] || exit 1
echo "tar selftest: OK"
