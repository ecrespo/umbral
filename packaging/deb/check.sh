#!/usr/bin/env bash
# Verifies a built umbral_*.deb (T-PKG-03, REQ-PKG-001, REQ-PKG-002, REQ-PKG-003).
#
#   packaging/deb/check.sh umbral_<version>_<arch>.deb
#
# What a user's machine gets is the package, not the branding kit: the `icons` job proves the
# kit, this proves the kit reached the package. It checks
#   1. the 11 hicolor files of REQ-PKG-001 and the .desktop file of REQ-PKG-002 are listed by
#      `dpkg -c` at their installed paths, byte for byte the kit's;
#   2. the three binaries are in /usr/bin;
#   3. the packaged .desktop passes desktop-file-validate and carries the release 0.1 launcher
#      (REQ-PKG-003);
#   4. postinst and postrm exist and refresh the icon cache and the desktop database;
#   5. the `systemd --user` unit is at usr/lib/systemd/user/umbrald.service, byte for byte
#      packaging/systemd's, passes `systemd-analyze verify`, and nothing enables it (T-PKG-06).
set -euo pipefail

deb="${1:?usage: check.sh PATH_TO_DEB}"
[[ -f "$deb" ]] || { echo "deb: $deb does not exist" >&2; exit 1; }
deb="$(realpath "$deb")"

cd "$(dirname "$0")/../.."
KIT="assets/branding/umbral-icons/linux/share"
ID="io.github.ecrespo.Umbral"

expected=(
  "usr/share/applications/$ID.desktop"
  "usr/share/icons/hicolor/scalable/apps/$ID.svg"
  "usr/share/icons/hicolor/symbolic/apps/$ID-symbolic.svg"
)
for size in 16 22 24 32 48 64 128 256 512; do
  expected+=("usr/share/icons/hicolor/${size}x${size}/apps/$ID.png")
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
dpkg-deb -c "$deb" | awk '{print $NF}' | sed 's|^\./||' > "$work/listing"
dpkg-deb -R "$deb" "$work/root"

failed=0
fail() { echo "deb: $*" >&2; failed=1; }

echo "deb: 1/5 icons and .desktop listed and identical to the kit"
icons=0
for path in "${expected[@]}"; do
  if ! grep -qxF "$path" "$work/listing"; then
    fail "$path is not in the package"
    continue
  fi
  [[ "$path" == usr/share/icons/* ]] && icons=$((icons + 1))
  if ! cmp -s "$work/root/$path" "$KIT/${path#usr/share/}"; then
    fail "$path differs from the branding kit"
  fi
done
[[ $icons -eq 11 ]] || fail "$icons icon files where REQ-PKG-001 names 11"

echo "deb: 2/5 binaries"
for bin in umbrald umb umbral-tui; do
  [[ -x "$work/root/usr/bin/$bin" ]] || fail "usr/bin/$bin is missing or not executable"
done

echo "deb: 3/5 the packaged .desktop file"
desktop="$work/root/usr/share/applications/$ID.desktop"
if ! command -v desktop-file-validate >/dev/null; then
  fail "desktop-file-validate is not installed (desktop-file-utils)"
elif [[ -f "$desktop" ]]; then
  desktop-file-validate "$desktop" || fail "desktop-file-validate rejects the packaged file"
  for line in "Icon=$ID" "StartupWMClass=$ID" "Exec=umbral-tui" "Terminal=true"; do
    grep -qxF "$line" "$desktop" || fail "the packaged .desktop lacks $line"
  done
fi

echo "deb: 4/5 maintainer scripts"
for script in postinst postrm; do
  file="$work/root/DEBIAN/$script"
  if [[ ! -x "$file" ]]; then
    fail "DEBIAN/$script is missing or not executable"
    continue
  fi
  for tool in gtk-update-icon-cache update-desktop-database; do
    grep -q "$tool" "$file" || fail "DEBIAN/$script does not run $tool"
  done
done

echo "deb: 5/5 the systemd --user unit, not enabled"
unit="usr/lib/systemd/user/umbrald.service"
if ! grep -qxF "$unit" "$work/listing"; then
  fail "$unit is not in the package"
else
  cmp -s "$work/root/$unit" packaging/systemd/umbrald.service || fail "$unit differs from packaging/systemd"
  grep -qxF "ExecStart=/usr/bin/umbrald" "$work/root/$unit" || fail "$unit does not run /usr/bin/umbrald"
  if command -v systemd-analyze >/dev/null; then
    # verify resolves ExecStart on this machine, so it reads the unit with the packaged binary.
    sed "s|^ExecStart=/usr/bin/umbrald|ExecStart=$work/root/usr/bin/umbrald|" "$work/root/$unit" > "$work/umbrald.service"
    systemd-analyze --user verify "$work/umbrald.service" || fail "systemd-analyze rejects $unit"
  fi
fi
# Not enabled by default: no .wants link shipped, and no maintainer script enables or starts it.
if grep -q '\.wants/' "$work/listing"; then
  fail "the package ships a .wants link, which enables a unit"
fi
for script in "$work/root/DEBIAN/"*; do
  if grep -qE 'systemctl|deb-systemd' "$script"; then
    fail "DEBIAN/$(basename "$script") calls systemctl; the unit is the user's to enable"
  fi
done

if [[ $failed -ne 0 ]]; then
  echo "deb: FAILED" >&2
  exit 1
fi
echo "deb: OK ($(basename "$deb"))"
