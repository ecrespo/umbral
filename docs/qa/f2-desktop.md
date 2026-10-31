# QA checklist — the desktop client (F2c)

The automated checks drive the window under xvfb, an X server with no window manager and no
dock. What they cannot cover is a real desktop session: whether GNOME or KDE on Wayland
matches the window to the installed `.desktop` file and shows the kit's icon. F2c's exit
criterion is this walk with no failing step (Plan §4). T-PKG-04 started it; the other F2c
tasks add their steps before the slice closes.

## Setup

1. Build: `task deps:ghostty` once, then `task build` and `task desktop` (needs docker).
2. Install the kit's icons and `.desktop` file for your user, as the kit's README says:
   `cp -r assets/branding/umbral-icons/linux/share/icons/hicolor/* ~/.local/share/icons/hicolor/`,
   `cp assets/branding/umbral-icons/linux/share/applications/io.github.ecrespo.Umbral.desktop ~/.local/share/applications/`,
   then `gtk-update-icon-cache -f -t ~/.local/share/icons/hicolor`.
3. Log in to a Wayland session (GNOME or KDE Plasma).

## Steps

### 1. The window carries the kit's id and icon (REQ-PKG-004)

1. Run `WAYLAND_DEBUG=client bin/umbral-desktop 2>&1 | grep set_app_id`.
   **Expect** `xdg_toplevel#N.set_app_id("io.github.ecrespo.Umbral")`.
2. Look at the dock or the task switcher while the window is open.
   **Expect** the kit's Umbral icon, not a generic one, and one entry, not two.
3. Save a screenshot of the dock as `docs/qa/f2-desktop-dock.png`.

On 2026-10-31, in a GNOME Wayland session on Ubuntu 26.04 with GTK 4.22.4, step 1 printed
`set_app_id("io.github.ecrespo.Umbral")` (delta `2026-10-wails-icons`). Steps 2 and 3 have not
been walked yet.
