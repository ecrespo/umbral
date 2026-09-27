#!/usr/bin/env bash
# Verifies the branding kit in assets/branding/umbral-icons (REQ-PKG-001, 002, 003, 006).
#
# Three things are checked, in order:
#   1. every committed artifact matches CHECKSUMS.sha256;
#   2. regenerating the kit from tools/build.py reproduces those artifacts byte for byte;
#   3. the .desktop file passes desktop-file-validate and carries the release 0.1 launcher.
#
# REQ-PKG-006 is the reason step 2 exists: a build pipeline that regenerates an icon from
# appicon.png must fail rather than silently ship a different image. Step 1 alone would not
# catch a source edit that was committed together with its new checksum.
#
# `--update` rewrites CHECKSUMS.sha256 after a deliberate change to the kit. Finding A-15
# exists because the .desktop file is covered by the checksums, so editing it without this
# leaves the `icons` job red.
set -euo pipefail

cd "$(dirname "$0")/.."
KIT="assets/branding/umbral-icons"
DESKTOP="$KIT/linux/share/applications/io.github.ecrespo.Umbral.desktop"

# Files the checksums cover: everything the build produces. tools/ is the source, README.md
# is prose, and preview/ comes from sheet.py rather than build.py.
checksummed_files() {
  find . -type f \
    ! -name 'CHECKSUMS.sha256' \
    ! -name 'README.md' \
    ! -path './tools/*' \
    ! -path './preview/*' \
    -print | LC_ALL=C sort
}

if [[ "${1:-}" == "--update" ]]; then
  ( cd "$KIT" && checksummed_files | xargs sha256sum > CHECKSUMS.sha256 )
  echo "icons: CHECKSUMS.sha256 rewritten ($(wc -l < "$KIT/CHECKSUMS.sha256") files)"
  exit 0
fi

echo "icons: 1/3 verifying checksums"
( cd "$KIT" && sha256sum -c --quiet CHECKSUMS.sha256 )

echo "icons: 2/3 regenerating the kit and comparing"
if ! python3 -c 'import cairosvg, PIL' 2>/dev/null; then
  echo "icons: SKIPPED the regeneration check (cairosvg and Pillow are not installed)" >&2
  echo "icons: install them with: pip install cairosvg Pillow" >&2
else
  rebuilt="$(mktemp -d)"
  trap 'rm -rf "$rebuilt"' EXIT
  ( cd "$KIT/tools" && python3 build.py --out "$rebuilt" >/dev/null )

  failed=0
  while IFS= read -r relative; do
    generated="$rebuilt/${relative#./}"
    # preview/ and the sources under tools/ are not produced by build.py.
    [[ -f "$generated" ]] || continue
    if ! cmp -s "$generated" "$KIT/$relative"; then
      echo "icons: $relative differs from what tools/build.py produces" >&2
      failed=1
    fi
  done < <( cd "$KIT" && checksummed_files )
  [[ "$failed" -eq 0 ]] || exit 1
fi

echo "icons: 3/3 validating the .desktop file"
if command -v desktop-file-validate >/dev/null 2>&1; then
  desktop-file-validate "$DESKTOP"
else
  echo "icons: SKIPPED desktop-file-validate (not installed)" >&2
fi

# REQ-PKG-002 and REQ-PKG-003, asserted rather than left to the validator, which does not
# know which binary release 0.1 ships.
for required in \
  'Exec=umbral-tui' \
  'Terminal=true' \
  'Icon=io.github.ecrespo.Umbral' \
  'StartupWMClass=io.github.ecrespo.Umbral'
do
  if ! grep -qxF "$required" "$DESKTOP"; then
    echo "icons: the .desktop file is missing '$required'" >&2
    exit 1
  fi
done

echo "icons: OK"
