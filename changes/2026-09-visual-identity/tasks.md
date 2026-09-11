# Tasks — Delta 2026-09-visual-identity

> Delta: `changes/2026-09-visual-identity/delta-spec.md` · Generated: 2026-09-11

## Tasks

### [ ] T-PKG-01 · Incorporate the kit and pin checksums
- **What:**
  - copy the kit to `assets/branding/umbral-icons/`;
  - generate `CHECKSUMS.sha256` for every binary;
  - create the CI job `icons` (regenerate with `tools/build.py` and compare).
- **REQ:** REQ-PKG-001, REQ-PKG-006
- **Depends on:** —
- **Done:** `icons` job green; changing one pixel of a PNG turns the job red.

### [ ] T-PKG-02 · `.desktop` file for release 0.1
- **What:** set the `.desktop` file to `Exec=umbral-tui` and `Terminal=true`, and validate it in CI.
- **REQ:** REQ-PKG-002, REQ-PKG-003
- **Depends on:** T-PKG-01
- **Done:** `desktop-file-validate share/applications/io.github.ecrespo.Umbral.desktop` without errors.

### [ ] T-PKG-03 · Installation in the `.deb`
- **What:** include the hicolor tree and the `.desktop` file in the package; postinst hooks/triggers refresh the caches (`gtk-update-icon-cache`, `update-desktop-database`).
- **REQ:** REQ-PKG-001, REQ-PKG-002
- **Depends on:** T-PKG-02
- **Done:** `dpkg -c umbral_*.deb` lists the 11 icon files and the `.desktop` file; smoke test in an Ubuntu 26.04 container.

### [ ] T-PKG-04 · Wails v3: icons, app_id and Taskfile override (F2)
- **What:**
  - replace the `generate:icons` task so it copies `wails-build/` instead of regenerating;
  - set the application and window app-id;
  - embed the `.ico`.
- **REQ:** REQ-PKG-004, REQ-PKG-005, REQ-PKG-006
- **Depends on:** T-PKG-01, F2 desktop client
- **Done:**
  - on GNOME Wayland, the dock shows the kit icon for the open window (manual check with a screenshot in `docs/qa/`);
  - inspecting the `.exe` lists the 8 `.ico` entries.

### [ ] T-PKG-05 · macOS Tahoe: `.icon` and `Assets.car` (F2)
- **What:** assemble `appicon.icon` in Icon Composer from the layers; compile with `wails3 generate icons -iconcomposerinput`; validate dark/light/tinted modes.
- **REQ:** REQ-PKG-007
- **Depends on:** T-PKG-04
- **Done:** `Info.plist` with `CFBundleIconName = appicon`; on macOS 26 the Dock does not show the generic tile (screenshot in `docs/qa/`).

## Traceability matrix

| REQ | Tasks | Verification |
|---|---|---|
| REQ-PKG-001 | T-PKG-01, T-PKG-03 | `icons` job, `dpkg -c` |
| REQ-PKG-002 | T-PKG-02, T-PKG-03 | `desktop-file-validate` |
| REQ-PKG-003 | T-PKG-02 | `.desktop` review in CI |
| REQ-PKG-004 | T-PKG-04 | manual QA on GNOME Wayland |
| REQ-PKG-005 | T-PKG-04 | embedded `.ico` inspection |
| REQ-PKG-006 | T-PKG-01, T-PKG-04 | checksums in CI |
| REQ-PKG-007 | T-PKG-05 | manual QA on macOS 26 |
| REQ-PKG-008 | — | SHOULD, deferred |
