#!/usr/bin/env bash
# Proves packaging/deb/check.sh rejects a broken package (T-PKG-03).
#
#   packaging/deb/check_selftest.sh umbral_<version>_<arch>.deb
#
# A checker that passes everything passes silently, so each case below rebuilds the good
# package with one defect and asserts check.sh fails on it: a missing icon, an icon that is
# not the kit's, a .desktop entry naming another icon, a postinst that refreshes nothing, a
# missing or altered systemd --user unit and a postinst that enables it;
# and the versions build.sh writes must sort as releases do.
set -euo pipefail

deb="${1:?usage: check_selftest.sh PATH_TO_DEB}"
here="$(cd "$(dirname "$0")" && pwd)"
ID="io.github.ecrespo.Umbral"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

"$here/check.sh" "$deb" >/dev/null || { echo "deb selftest: the good package fails" >&2; exit 1; }

failed=0
mutant() {
  local name="$1" edit="$2"
  rm -rf "$work/root" && dpkg-deb -R "$deb" "$work/root"
  ( cd "$work/root" && eval "$edit" )
  dpkg-deb --root-owner-group --build "$work/root" "$work/mutant.deb" >/dev/null
  if "$here/check.sh" "$work/mutant.deb" >/dev/null 2>&1; then
    echo "deb selftest: check.sh accepted a package with $name" >&2
    failed=1
  else
    echo "deb selftest: rejected $name"
  fi
}

mutant "the 64 px icon missing" "rm usr/share/icons/hicolor/64x64/apps/$ID.png"
mutant "a symbolic icon not the kit's" "echo '<svg/>' > usr/share/icons/hicolor/symbolic/apps/$ID-symbolic.svg"
mutant "a .desktop naming another icon" "sed -i 's/^Icon=.*/Icon=utilities-terminal/' usr/share/applications/$ID.desktop"
mutant "a postinst that refreshes nothing" "sed -i '/gtk-update-icon-cache/d' DEBIAN/postinst"
mutant "no systemd --user unit" "rm usr/lib/systemd/user/umbrald.service"
mutant "a unit running another binary" "sed -i 's|^ExecStart=.*|ExecStart=/usr/bin/umb status|' usr/lib/systemd/user/umbrald.service"
mutant "a postinst that enables the unit" "echo 'systemctl --global enable umbrald.service' >> DEBIAN/postinst"

# The version scheme: each pair must sort as a release would, the untagged hash that starts
# with a digit included, or apt would never upgrade from a development build to 0.1.0.
version_of() { "$here/build.sh" --version-of "$1"; }
sorts_below() {
  local low high
  low="$(version_of "$1")" high="$(version_of "$2")"
  if dpkg --compare-versions "$low" lt "$high"; then
    echo "deb selftest: $1 ($low) sorts below $2 ($high)"
  else
    echo "deb selftest: $1 ($low) does not sort below $2 ($high)" >&2
    failed=1
  fi
}
sorts_below 7f3e2a1 v0.1.0
sorts_below e0a93d1-dirty v0.0.1
sorts_below v0.1.0-rc.1 v0.1.0
sorts_below v0.1.0-rc.1-2-gabc1234 v0.1.0
sorts_below v0.1.0 v0.1.0-3-gabc1234
sorts_below v0.1.0-3-gabc1234 v0.1.1

[[ $failed -eq 0 ]] || exit 1
echo "deb selftest: OK"
