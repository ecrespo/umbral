# Delta — Visual identity and application icon

New REQs take new IDs (`REQ-PKG-*`). No existing REQ is removed.

## ADDED

### specs/prd/umbral-mvp.md → §6.10 Packaging and identity (PKG)

- **REQ-PKG-001** · MUST · ubiquitous — THE SYSTEM SHALL install its icon in the `hicolor` theme with the name `io.github.ecrespo.Umbral`, in `scalable/apps` (SVG) and as PNG at 16, 22, 24, 32, 48, 64, 128, 256 and 512 px, plus `symbolic/apps/io.github.ecrespo.Umbral-symbolic.svg`.
- **REQ-PKG-002** · MUST · ubiquitous — THE SYSTEM SHALL install `io.github.ecrespo.Umbral.desktop` with `Icon=io.github.ecrespo.Umbral` and `StartupWMClass=io.github.ecrespo.Umbral`, and that file SHALL pass `desktop-file-validate` without errors.
- **REQ-PKG-003** · MUST · optional — WHERE only the TUI is distributed (release 0.1), the `.desktop` file SHALL use `Exec=umbral-tui` with `Terminal=true`.
- **REQ-PKG-004** · MUST · event — WHEN the desktop client (F2) creates its window, THE SYSTEM SHALL set the Wayland `app_id` and the X11 `WM_CLASS` to `io.github.ecrespo.Umbral`.
- **REQ-PKG-005** · MUST · ubiquitous — THE SYSTEM SHALL embed in the Windows executable (F2) an `.ico` with entries at 16, 20, 24, 32, 40, 48, 64 and 256 px, where the 16 and 24 px entries come from the hand-drawn sources.
- **REQ-PKG-006** · MUST · unwanted — IF the Wails build pipeline tries to regenerate `icon.ico` or `icons.icns` from `appicon.png`, THEN the build SHALL use the kit's files and fail if their SHA-256 differ from `assets/branding/umbral-icons/CHECKSUMS.sha256`.
- **REQ-PKG-007** · MUST · ubiquitous — THE SYSTEM SHALL, on macOS (F2), include an `Assets.car` compiled from `appicon.icon` (Icon Composer) with `CFBundleIconName = appicon`, and a fallback `icons.icns` for macOS ≤ 15.
- **REQ-PKG-008** · SHOULD · ubiquitous — THE SYSTEM SHOULD provide an icon variant for nightly builds, distinguishable at 48 px.

### specs/technical/umbral-architecture.md → §9.3 Visual identity and packaging (new)

- **Single source:**
  - `assets/branding/umbral-icons/tools/svgs.py` generates every format (`tools/build.py`);
  - the 16 and 24 px sizes have their own sources;
  - 32 px and above use the 128 master.
- **Linux:** `share/icons/hicolor` tree plus the `.desktop` file; the Breeze variant is optional and not installed by default.
- **Windows:** `umbral.ico` + MSIX `Assets/` (`Square44x44Logo` with scales and `targetsize-*_altform-unplated`).
- **macOS:**
  - the layers in `macos/icon-composer-layers/` are assembled in Icon Composer → `build/appicon.icon`;
  - Wails compiles it with `actool` (Xcode 26);
  - `umbral.icns` is the fallback.
- **CI:** `icons` job that regenerates the kit, compares checksums and validates the `.desktop` file.

### specs/plans/umbral-mvp-plan.md → Hardening 0.1 and F2

- Hardening 0.1: add T-PKG-01 … T-PKG-03.
- F2: add T-PKG-04 and T-PKG-05.

## MODIFIED

### assets/branding/umbral-icons/linux/share/applications/io.github.ecrespo.Umbral.desktop
- **Before:** `Exec=umbral-desktop %U`, `Terminal=false`.
- **After (0.1):** `Exec=umbral-tui`, `Terminal=true` (REQ-PKG-003). In F2 it goes back to `umbral-desktop` through a new delta.
- **Reason:** there is no desktop client in 0.1.

## REMOVED

— (none)
