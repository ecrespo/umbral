#!/usr/bin/env bash
# Builds umbral_<version>_<arch>.deb from binaries already built (T-PKG-03).
#
#   packaging/deb/build.sh BIN_DIR VERSION OUT_DIR
#   packaging/deb/build.sh --version-of GIT_DESCRIBE    (prints the Debian version only)
#
# BIN_DIR holds umbrald, umb and umbral-tui; VERSION is what `git describe` printed. The icons
# and the .desktop file are copied from the branding kit as they are — REQ-PKG-006 forbids
# regenerating them — so the package ships exactly what the `icons` job verified.
#
# libghostty-vt is linked statically (its -static pkg-config file), so the only shared library
# the binaries load is libc; Depends names the newest glibc symbol version they reference.
set -euo pipefail

# deb_version maps what `git describe --tags --always --dirty` printed to a Debian version that
# sorts the way releases do. Only a tag shaped vMAJOR.MINOR.PATCH counts as a release:
#   v0.1.0                -> 0.1.0
#   v0.1.0-rc.1           -> 0.1.0~rc.1          (a prerelease sorts below its release)
#   v0.1.0-3-gabc1234     -> 0.1.0+3.gabc1234    (commits after a tag sort above it)
#   e0a93d1 / 7f3e2a1     -> 0.0.0+git.e0a93d1   (no tag: below every release, even a hash
#                                                 that happens to start with a digit)
# and a -dirty suffix becomes .dirty on the last part.
deb_version() {
  local v="${1#v}" re='^([0-9]+\.[0-9]+\.[0-9]+)(-([0-9A-Za-z.]+))?(-([0-9]+)-g([0-9a-f]+))?(-dirty)?$'
  if [[ "$v" =~ $re ]]; then
    local out="${BASH_REMATCH[1]}"
    [[ -n "${BASH_REMATCH[3]}" ]] && out+="~${BASH_REMATCH[3]}"
    [[ -n "${BASH_REMATCH[4]}" ]] && out+="+${BASH_REMATCH[5]}.g${BASH_REMATCH[6]}"
    if [[ -n "${BASH_REMATCH[7]}" ]]; then
      [[ "$out" == *+* ]] && out+=".dirty" || out+="+dirty"
    fi
    printf '%s\n' "$out"
  else
    printf '0.0.0+git.%s\n' "${v//-/.}"
  fi
}

if [[ "${1:-}" == "--version-of" ]]; then
  deb_version "${2:?usage: build.sh --version-of GIT_DESCRIBE}"
  exit 0
fi

bin_dir="${1:?usage: build.sh BIN_DIR VERSION OUT_DIR}"
version="${2:?usage: build.sh BIN_DIR VERSION OUT_DIR}"
out_dir="${3:?usage: build.sh BIN_DIR VERSION OUT_DIR}"

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
KIT="$root/assets/branding/umbral-icons/linux/share"

deb_version="$(deb_version "$version")"

# The binaries are built for the host unless ARCH says otherwise; a cross build sets it.
arch="${ARCH:-$(dpkg --print-architecture)}"

# Modes and mtimes come from here, not from the builder's umask and clock, so two builds of
# one commit produce the same package.
umask 022
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$root" log -1 --format=%ct)}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
chmod 0755 "$stage"

install -d "$stage/DEBIAN" "$stage/usr/bin" "$stage/usr/share/doc/umbral"
for bin in umbrald umb umbral-tui; do
  install -m 0755 "$bin_dir/$bin" "$stage/usr/bin/$bin"
done
( cd "$KIT" && find applications icons/hicolor -type f ) | while IFS= read -r file; do
  install -D -m 0644 "$KIT/$file" "$stage/usr/share/$file"
done
install -m 0644 "$root/LICENSE" "$stage/usr/share/doc/umbral/copyright"

glibc="$(for bin in umbrald umb umbral-tui; do
  objdump -T "$stage/usr/bin/$bin" | grep -o 'GLIBC_[0-9.]*' || true
done | sed 's/GLIBC_//' | sort -uV | tail -1)"

[[ -n "$glibc" ]] || { echo "build.sh: no GLIBC symbol version found in the binaries" >&2; exit 1; }

installed_kib="$(du -sk --exclude=DEBIAN "$stage" | cut -f1)"

cat > "$stage/DEBIAN/control" <<EOF
Package: umbral
Version: $deb_version
Architecture: $arch
Maintainer: Ernesto Crespo <ecrespo@users.noreply.github.com>
Installed-Size: $installed_kib
Depends: libc6 (>= $glibc)
Recommends: git
Section: utils
Priority: optional
Homepage: https://github.com/ecrespo/umbral
Description: local-first agentic terminal
 Umbral is a terminal whose daemon, umbrald, owns PTY sessions, command blocks,
 an agent with local or remote models, and MCP servers. umbral-tui and umb are
 thin clients of that daemon.
EOF
install -m 0755 "$here/postinst" "$here/postrm" "$stage/DEBIAN/"

find "$stage" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +

mkdir -p "$out_dir"
deb="$out_dir/umbral_${deb_version}_${arch}.deb"
dpkg-deb --root-owner-group -Zxz --build "$stage" "$deb" >/dev/null
echo "$deb"
