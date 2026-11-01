#!/usr/bin/env bash
# Verifies a built umbral-<version>-<os>-<arch>.tar.gz (T-PKG-07).
#
#   packaging/tar/check.sh umbral-<version>-<os>-<arch>.tar.gz
#
# It unpacks the archive and checks
#   1. one top directory named as the archive and no AppleDouble (`._*`) entries, holding bin/
#      with umbrald, umb and umbral-tui, each of which prints the archive's version when the
#      archive was built for this machine;
#   2. on a linux archive, the kit's share/ tree — the 11 hicolor files of REQ-PKG-001 and the
#      .desktop file of REQ-PKG-002 — byte for byte the kit's;
#   3. on a darwin archive, the launchd agent plist, which passes `plutil -lint` where plutil
#      exists and runs bin/umbrald's installed path.
set -euo pipefail

archive="${1:?usage: check.sh ARCHIVE.tar.gz}"
[[ -f "$archive" ]] || { echo "tar: $archive does not exist" >&2; exit 1; }
archive="$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")"

cd "$(dirname "$0")/../.."
KIT="assets/branding/umbral-icons/linux/share"
ID="io.github.ecrespo.Umbral"
LABEL="io.github.ecrespo.umbral.umbrald"

top="$(basename "$archive" .tar.gz)"
re='^umbral-.+-(linux|darwin)-(amd64|arm64)$'
[[ "$top" =~ $re ]] || { echo "tar: $top is not umbral-<version>-<linux|darwin>-<amd64|arm64>" >&2; exit 1; }
os="${BASH_REMATCH[1]}" arch="${BASH_REMATCH[2]}"
version="${top#umbral-}"
version="${version%-"$os"-"$arch"}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
tar -xzf "$archive" -C "$work"

failed=0
fail() { echo "tar: $*" >&2; failed=1; }

echo "tar: 1/3 one top directory and the three binaries"
# The listing is read whole first: under pipefail, `grep -q` quitting early would fail tar.
listing="$(tar -tzf "$archive")"
if grep -qE '(^|/)\._' <<<"$listing"; then
  fail "the archive carries AppleDouble (._*) entries"
fi
tops="$(cd "$work" && ls -A)"
[[ "$tops" == "$top" ]] || fail "the archive holds ${tops//$'\n'/ } where it should hold $top/ alone"
root="$work/$top"
host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_arch="$(uname -m)"; host_arch="${host_arch/x86_64/amd64}"; host_arch="${host_arch/aarch64/arm64}"
for bin in umbrald umb umbral-tui; do
  if [[ ! -x "$root/bin/$bin" ]]; then
    fail "bin/$bin is missing or not executable"
    continue
  fi
  if [[ "$os" == "$host_os" && "$arch" == "$host_arch" ]]; then
    out=""
    case "$bin" in
      umb) out="$("$root/bin/$bin" version --no-autostart 2>&1)" || fail "bin/umb version failed: $out" ;;
      *) out="$("$root/bin/$bin" -version 2>&1)" || fail "bin/$bin -version failed: $out" ;;
    esac
    if [[ "$out" != *"$version"* ]]; then
      fail "bin/$bin printed ${out:-nothing} where the archive's version is $version"
    else
      echo "tar:   $bin: ${out%%$'\n'*}"
    fi
  fi
done
[[ -f "$root/LICENSE" ]] || fail "LICENSE is missing"

if [[ "$os" == linux ]]; then
  echo "tar: 2/3 the kit's share/ tree"
  count=0
  for path in "applications/$ID.desktop" "icons/hicolor/scalable/apps/$ID.svg" \
    "icons/hicolor/symbolic/apps/$ID-symbolic.svg" \
    $(for s in 16 22 24 32 48 64 128 256 512; do echo "icons/hicolor/${s}x${s}/apps/$ID.png"; done); do
    if [[ ! -f "$root/share/$path" ]]; then
      fail "share/$path is missing"
    elif ! cmp -s "$root/share/$path" "$KIT/$path"; then
      fail "share/$path differs from the branding kit"
    else
      [[ "$path" == icons/* ]] && count=$((count + 1))
    fi
  done
  [[ $count -eq 11 ]] || fail "$count kit icons where REQ-PKG-001 names 11"
else
  echo "tar: 2/3 no share/ tree on darwin"
fi

if [[ "$os" == darwin ]]; then
  echo "tar: 3/3 the launchd agent plist"
  plist="$root/share/launchd/$LABEL.plist"
  if [[ ! -f "$plist" ]]; then
    fail "share/launchd/$LABEL.plist is missing"
  else
    cmp -s "$plist" "packaging/launchd/$LABEL.plist" || fail "the plist differs from packaging/launchd"
    if command -v plutil >/dev/null; then
      plutil -lint "$plist" >/dev/null || fail "plutil -lint rejects the plist"
    fi
    grep -q "<string>$LABEL</string>" "$plist" || fail "the plist's Label is not $LABEL"
    grep -q "<string>/usr/local/bin/umbrald</string>" "$plist" || fail "the plist does not run /usr/local/bin/umbrald"
  fi
else
  echo "tar: 3/3 no launchd plist on linux"
  [[ ! -e "$root/share/launchd" ]] || fail "a linux archive carries share/launchd"
fi

if [[ $failed -ne 0 ]]; then
  echo "tar: FAILED" >&2
  exit 1
fi
echo "tar: OK ($(basename "$archive"))"
