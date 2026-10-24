#!/usr/bin/env bash
# Installs a built umbral_*.deb in a clean Ubuntu 26.04 container and uses it (T-PKG-03).
#
#   packaging/deb/smoke.sh umbral_<version>_<arch>.deb
#
# apt resolves Depends, so a package needing a newer libc than the release has fails here, and
# umb autostarts the installed umbrald and reads its status.
# hicolor-icon-theme and desktop-file-utils are installed first, so postinst finds both tools
# and the icon cache it refreshes can be read back; then the package is removed, and postrm must
# leave that cache without its icon. update-desktop-database writes only MIME associations,
# and the entry declares none, so there is nothing of it to read back.
set -euo pipefail

deb="${1:?usage: smoke.sh PATH_TO_DEB}"
image="${UMBRAL_SMOKE_IMAGE:-ubuntu:26.04}"
name="$(basename "$deb")"

docker run --rm --network=bridge -v "$(cd "$(dirname "$deb")" && pwd)/$name:/tmp/$name:ro" \
  -e DEB="/tmp/$name" "$image" bash -euo pipefail -c '
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq hicolor-icon-theme desktop-file-utils libgtk-3-bin >/dev/null
    apt-get install -y -qq "$DEB" >/dev/null

    id=io.github.ecrespo.Umbral
    for bin in umbrald umb umbral-tui; do command -v "$bin" >/dev/null; done
    umb version --no-autostart

    # The daemon runs on this release: umb autostarts it from PATH and reads its status.
    export XDG_RUNTIME_DIR=/tmp/rt && mkdir -m 0700 -p "$XDG_RUNTIME_DIR"
    umb status
    pkill -x umbrald
    test -f /usr/share/applications/$id.desktop
    test "$(find /usr/share/icons/hicolor -name "$id*" | wc -l)" -eq 11

    grep -q "$id" /usr/share/icons/hicolor/icon-theme.cache

    apt-get remove -y -qq umbral >/dev/null
    test ! -e /usr/bin/umbrald
    if grep -qs "$id" /usr/share/icons/hicolor/icon-theme.cache; then
      echo "smoke: the icon cache still lists $id after remove" >&2
      exit 1
    fi
    echo "smoke: OK"
  '
