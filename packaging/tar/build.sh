#!/usr/bin/env bash
# Builds umbral-<version>-<os>-<arch>.tar.gz from binaries already built (T-PKG-07).
#
#   packaging/tar/build.sh BIN_DIR VERSION OUT_DIR
#
# BIN_DIR holds umbrald, umb and umbral-tui built for GOOS/GOARCH (the host's unless set);
# VERSION is what `git describe` printed, without a leading v. The archive holds one directory
# of the same name: bin/ with the binaries, LICENSE, a README, and
#   linux:  share/ with the kit's .desktop file and hicolor icons, as they are (REQ-PKG-006);
#   darwin: share/launchd/ with the launchd agent plist.
set -euo pipefail

bin_dir="${1:?usage: build.sh BIN_DIR VERSION OUT_DIR}"
version="${2:?usage: build.sh BIN_DIR VERSION OUT_DIR}"
out_dir="${3:?usage: build.sh BIN_DIR VERSION OUT_DIR}"
version="${version#v}"

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
KIT="$root/assets/branding/umbral-icons/linux/share"
LABEL="io.github.ecrespo.umbral.umbrald"

os="${GOOS:-$(go env GOOS)}"
arch="${GOARCH:-$(go env GOARCH)}"
case "$os" in linux | darwin) ;; *) echo "build.sh: no archive for $os" >&2; exit 1 ;; esac

name="umbral-$version-$os-$arch"
umask 022
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$root" log -1 --format=%ct)}"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
dir="$stage/$name"

install -d "$dir/bin"
for bin in umbrald umb umbral-tui; do
  install -m 0755 "$bin_dir/$bin" "$dir/bin/$bin"
done
install -m 0644 "$root/LICENSE" "$dir/LICENSE"

if [[ "$os" == linux ]]; then
  ( cd "$KIT" && find applications icons/hicolor -type f ) | while IFS= read -r file; do
    install -d "$dir/share/$(dirname "$file")"
    install -m 0644 "$KIT/$file" "$dir/share/$file"
  done
  cat > "$dir/README" <<'TXT'
Umbral: copy bin/* to a directory on PATH, such as ~/.local/bin, and share/ to ~/.local/share
for the launcher and its icon. `umb` starts umbrald when it is not running.
TXT
else
  install -d "$dir/share/launchd"
  install -m 0644 "$root/packaging/launchd/$LABEL.plist" "$dir/share/launchd/$LABEL.plist"
  cat > "$dir/README" <<TXT
Umbral: copy bin/* to /usr/local/bin, which may need creating and sudo. The binaries are
unsigned in 0.1; a downloaded copy is quarantined until you run
  xattr -d com.apple.quarantine /usr/local/bin/umbrald /usr/local/bin/umb /usr/local/bin/umbral-tui
\`umb\` starts umbrald when it is not running. To start it at login instead, as a launchd agent:

  cp share/launchd/$LABEL.plist ~/Library/LaunchAgents/
  launchctl bootstrap gui/\$(id -u) ~/Library/LaunchAgents/$LABEL.plist

launchd does not restart it; \`umb\` starts it again if it stops. Its panes get launchd's
environment (set more with \`launchctl setenv\`), and it finds its socket from HOME, so a shell
that exports XDG_RUNTIME_DIR sends \`umb\` elsewhere: unset it there.
TXT
fi
chmod 0644 "$dir/README"

# Ownership and mtimes come from here, not from the builder, where the tar in use allows it.
# BSD touch has no -d @N, and reads -t in local time, so it is given UTC.
find "$stage" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} + 2>/dev/null ||
  TZ=UTC0 find "$stage" -exec touch -h -t "$(date -u -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)" {} +
mkdir -p "$out_dir"
out="$(cd "$out_dir" && pwd)/$name.tar.gz"
tar_version="$(tar --version 2>/dev/null || true)"
if [[ "$tar_version" == *GNU* ]]; then
  tar -C "$stage" --sort=name --owner=0 --group=0 --numeric-owner -cf - "$name" | gzip -n > "$out"
else
  # bsdtar on macOS: no AppleDouble (._*) entries and no extended attributes.
  COPYFILE_DISABLE=1 tar -C "$stage" --uid 0 --gid 0 --no-xattrs --no-mac-metadata -cf - "$name" | gzip -n > "$out"
fi
echo "$out"
