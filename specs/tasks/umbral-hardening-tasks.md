# Tasks — Umbral hardening 0.1 (packaging and identity)

> Source specs: `specs/prd/umbral-mvp.md` §6.14 · `specs/plans/umbral-mvp-plan.md` (hardening phase)
> Folded from `changes/_archive/2026-09-visual-identity/`. REQ-PKG-004, REQ-PKG-005, REQ-PKG-007
> and REQ-PKG-008 live in `specs/prd/umbral-f2-desktop.md` (finding A-12), together with their
> tasks T-PKG-04 and T-PKG-05.

## Conventions for this file

Same as `umbral-f0-tasks.md`. Packaging is verified with commands rather than Go tests, which is why
its matrix lists commands.

## Tasks

### [x] 2026-09-11 T-PKG-01 · Incorporate the kit and pin checksums
- **What:**
  - the kit lives in `assets/branding/umbral-icons/`;
  - `CHECKSUMS.sha256` covers every binary of the kit;
  - CI job `icons` that rebuilds with `tools/build.py` and compares.
- **REQ:** REQ-PKG-001, REQ-PKG-006
- **Files:** `assets/branding/umbral-icons/**`, `scripts/icons_check.sh`, `.github/workflows/ci.yml`, `Taskfile.yml`
- **Depends on:** —
- **Done:** the `icons` job is green; changing one pixel of a PNG turns it red.
- **Result:** `scripts/icons_check.sh` does three things rather than one: verify the
  checksums, regenerate with `build.py` and compare, then validate the `.desktop` file. The
  regeneration step is what REQ-PKG-006 actually asks for, because checking the checksums
  alone would pass a source edit committed together with its new checksum. Verified by
  flipping one byte of the 48 px PNG: exit 1. The kit reproduces byte for byte from source,
  so 55 of the 56 artifacts were already correct; only the `.desktop` file changed, in
  T-PKG-02. The regeneration step degrades to a warning when `cairosvg` and `Pillow` are
  missing, so a contributor without them still gets the checksum and `.desktop` checks; CI
  installs both, so the full check always runs there.

### [x] 2026-09-11 T-PKG-02 · `.desktop` file for release 0.1
- **What:** set `Exec=umbral-tui` and `Terminal=true`, validate it in CI, and regenerate
  `CHECKSUMS.sha256` in the same commit because the `.desktop` file is covered by it (finding A-15).
- **REQ:** REQ-PKG-002, REQ-PKG-003
- **Files:** `assets/branding/umbral-icons/linux/share/applications/**`, `assets/branding/umbral-icons/CHECKSUMS.sha256`
- **Depends on:** T-PKG-01
- **Done:** `desktop-file-validate` without errors and the `icons` job still green after the change.
- **Result:** the change went into the `DESKTOP` template in `build.py`, not into the
  generated file, so the next regeneration keeps it. `desktop-file-validate` exits 0; it
  emits one *hint* about `Categories` listing more than one main category, which is
  pre-existing, is not an error, and is left for whoever revisits the kit's categories.
  `scripts/icons_check.sh` additionally asserts the four lines REQ-PKG-002 and REQ-PKG-003
  name, because the validator does not know which binary release 0.1 ships. Checksums
  regenerated, closing A-15.

### [ ] T-PKG-03 · Installation in the `.deb`
- **What:** ship the hicolor tree and the `.desktop` file in the package; postinst triggers refresh
  `gtk-update-icon-cache` and `update-desktop-database`.
- **REQ:** REQ-PKG-001, REQ-PKG-002
- **Files:** `packaging/deb/**`
- **Depends on:** T-PKG-02
- **Done:** `dpkg -c umbral_*.deb` lists the 11 icon files and the `.desktop` file; smoke test in an
  Ubuntu 26.04 container.

## Traceability matrix (hardening)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-PKG-001 | T-PKG-01, T-PKG-03 | `icons` job, `dpkg -c` |
| REQ-PKG-002 | T-PKG-02, T-PKG-03 | `desktop-file-validate` |
| REQ-PKG-003 | T-PKG-02 | `.desktop` review in CI |
| REQ-PKG-006 | T-PKG-01 | the `icons` job regenerates the kit with `build.py` and compares byte for byte; checksums alone would pass a source edit committed with its new checksum |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-PKG-01, T-PKG-02 | done | Folded from `changes/_archive/2026-09-visual-identity/`. The `icons` CI job verifies the checksums, regenerates the kit from source with `tools/build.py` and compares byte for byte, then validates the `.desktop` file. The launcher targets `umbral-tui` with `Terminal=true`, since release 0.1 ships no desktop client. Recorded in the F0 tasks file until the hardening file existed; moved here on 2026-09-20 with the rest of the packaging work. |
