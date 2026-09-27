# Umbral — application icon kit v0.1

## Concept

*Umbral* is Spanish for "threshold": the stone step under a door, the exact point between inside
and outside. The icon draws it literally, in three pieces that survive at any size:

1. **Arched doorway**: the recognizable silhouette, even at 16 px.
2. **Inner light** (amber, radial gradient from the floor): the place where work happens.
3. **Prompt `>_`** standing in the light, on the **threshold** (the step), where the light spills out.

The prompt says "terminal"; the door and the step say "Umbral" and set it apart from other terminal
icons, which are almost always a window with `>_`.

Alternative concept explored and discarded: *Penumbra* (`source/concept-b-penumbra.svg`), a circle
half shadow, half light, with the cursor as the dividing line. It works as a brand mark, but at
16 px it is confused with brightness/contrast icons and loses the "terminal" reading.

## Palette

| Role | Hex |
|---|---|
| Night indigo (background, prompt) | `#100D2E` `#16123D` `#1E1A52` `#2B2670` |
| Frame indigo / highlight | `#3A34A0` `#6A63D0` |
| Threshold stone | `#5A53B8` |
| Light | `#FFF6E0` → `#FFD27A` → `#FFB443` → `#EE861A` |
| Symbolic | `#241F31` (recolored by the system) |

## Which file goes to each platform

| Platform | Required style | Kit file |
|---|---|---|
| GNOME | simple geometric object on a 128 px grid (2 px), no tile background, plus a 16 px symbolic | `linux/share/icons/hicolor/scalable/apps/<app-id>.svg` + `symbolic/apps/<app-id>-symbolic.svg` |
| KDE Plasma | reads the same hicolor theme; the native Breeze style uses a 48 px base, 4 px margins, relief and a 45° shadow | hicolor (default) · `linux/kde-breeze/` for KDE-specific packaging |
| XFCE / LXDE / MATE | freedesktop hicolor theme; fixed-size PNGs for panels (16, 22, 24) | `linux/share/icons/hicolor/<N>x<N>/apps/<app-id>.png` |
| Windows 10/11 | silhouette without a plate, soft corners, 3:1 contrast on light and dark themes; ICO with at least 16/24/32/48/256 | `windows/umbral.ico` + `windows/Assets/` (MSIX) |
| macOS ≤ 15 | 824 px tile on a 1024 canvas with a shadow | `macos/umbral.icns` |
| macOS 26 Tahoe | square 1024 layers **without a mask**; the system applies the shape, Liquid Glass and dark/light/tinted modes | `macos/icon-composer-layers/*.svg` → Icon Composer → `.icon` |

## Installing on Linux (local test, no root)

```bash
APP=io.github.ecrespo.Umbral
cp -r linux/share/icons/hicolor/* ~/.local/share/icons/hicolor/
cp linux/share/applications/$APP.desktop ~/.local/share/applications/
gtk-update-icon-cache -f -t ~/.local/share/icons/hicolor 2>/dev/null || true
update-desktop-database ~/.local/share/applications 2>/dev/null || true
```

For a system-wide install, copy to `/usr/share/...` (.deb/.rpm package) or let Flatpak do it with
the same `app-id`.

**The requirement that usually breaks the icon on Wayland**: the window's `app_id` must match the
`.desktop` file name (`io.github.ecrespo.Umbral.desktop`). If it does not, GNOME shows a generic icon
in the dock and in Alt+Tab even though the menu shows it correctly. On X11 the equivalent is
`StartupWMClass` (already included in the `.desktop` file).

## Windows

- **Wails**: copy `wails-build/windows/icon.ico` to `build/windows/`. Wails embeds it in the `.exe`
  through a `.syso`.
- **MSIX**: point the manifest's `Square44x44Logo`, `Square150x150Logo` and `StoreLogo` to
  `windows/Assets/`. The `targetsize-*_altform-unplated` files are the ones the taskbar uses without a
  plate.
- **Dark theme contrast**: the amber light provides the contrast; the indigo frame almost disappears
  on black (visible in the preview sheet). If the silhouette gets lost on a dark taskbar in real
  tests, Windows allows separate assets for light and dark themes: add a variant with the frame in
  `#6A63D0`.

## macOS

- **macOS 26 Tahoe (recommended)**:
  1. Open Icon Composer (ships with Xcode 26).
  2. Import `0-background.svg` as the background, then `1-spill-and-sill`, `2-door-light` and
     `3-prompt` as foreground layers, in that order.
  3. Let the system add the glass: do not add your own highlights or shadows.
  4. Check dark, light/clear and tinted modes, and save `appicon.icon`.
  5. With Wails v3, put it in `build/appicon.icon`. The icons task compiles it with `actool` into
     `Assets.car` + `icons.icns`, and `cfBundleIconName` must be set. Without `Assets.car`, Tahoe
     wraps the `.icns` in a generic tile.
- **Compatibility with earlier macOS**: `macos/umbral.icns` (also in `wails-build/darwin/icons.icns`).

## Wails v3: warning about automatic generation

The v3 Taskfile runs `wails3 generate icons -input appicon.png ...` and generates `.ico` and `.icns`
from **a single PNG**. That would give macOS the object icon instead of the tile. You have two
options:

- remove that step and use the files in `wails-build/`, or
- keep it only for Windows and give macOS the Icon Composer `.icon`.

## Regenerating

```bash
pip install cairosvg pillow
python3 tools/build.py --app-id io.github.ecrespo.Umbral --out dist
python3 tools/sheet.py          # preview sheet (expects dist/ in the current directory)
```

Everything comes from `tools/svgs.py`: changing a color or a proportion there regenerates every
format. The 16 and 24 px sizes have their own hand-drawn sources (`object_16`, `object_24`); from
32 px up, the 128 master is used.

## Pending before locking it as a brand

- [ ] Trademark/name search for "Umbral" in software (EUIPO, USPTO, app stores, Flathub).
- [ ] Pixel-level review of 22 px (today it is the 24 px scaled) on XFCE/KDE panels.
- [ ] Build the `.icon` in Icon Composer and validate Liquid Glass on a real Mac.
- [ ] Dark variant for the Windows taskbar if tests call for it.
- [ ] "Nightly" variant (GNOME recommends distinguishing development builds), e.g. with diagonal stripes on the step.
