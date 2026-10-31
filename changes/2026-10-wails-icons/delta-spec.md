# Delta — the desktop window's id and the Windows icon resource

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the Tech Lead: the scope ("si, apruebo") and the generator ("La opción 1") |
| **Date** | 2026-10-31 |
| **Task** | T-PKG-04 |
| **REQ** | REQ-PKG-004, REQ-PKG-005 (no change to their text) |

## Evidence

T-F2-30 set `io.github.ecrespo.Umbral` as the GTK application id and program name
(`cmd/umbral-desktop/options.go`) and left the test to T-PKG-04. The kit already ships the
Windows icon: `windows/umbral.ico`, identical to `wails-build/windows/icon.ico`. It has entries at
16, 20, 24, 32, 40, 48, 64 and 256 px. `tools/build.py` draws the 16 and 24 px entries from
`source/umbral-object-16.svg` and `umbral-object-24.svg`, and the branding job checks that the
kit is reproducible.

No Windows executable can be built yet. On 2026-10-31 a `GOOS=windows CGO_ENABLED=0` build of
`cmd/umbral-desktop` failed in two places. libghostty's Go binding needs cgo, and
`internal/config` uses `syscall.Stat_t`, which exists on Unix only. Windows support is T-F2-50
(F2e).

## Decisions

1. **REQ-PKG-004 is verified on the live window.** While the window is open,
   `scripts/desktop_smoke.sh` reads the X11 class of the window titled `Umbral` with `xwininfo`.
   The smoke fails unless the class is `("io.github.ecrespo.Umbral" "io.github.ecrespo.Umbral")`.
   CI's `desktop` job installs `x11-utils` for it. Measured on 2026-10-31 under xvfb, GTK 4.22.4:
   the class is that value. A build whose `AppID` was changed to `io.github.ecrespo.Mutant` showed
   the mutant class.
   The Wayland `app_id` is not observable under xvfb, which has no compositor. On 2026-10-31,
   in a GNOME Wayland session, `WAYLAND_DEBUG=client` showed the window sending
   `xdg_toplevel.set_app_id("io.github.ecrespo.Umbral")`. The run was hermetic: its own
   runtime directory and daemon, and the compositor reached through an absolute
   `WAYLAND_DISPLAY`. That check and the dock's icon are step 1 of `docs/qa/f2-desktop.md`,
   the walk F2c's exit criterion names (Plan §4). T-PKG-04 starts that walk.
2. **REQ-PKG-005 is split at the executable.** T-PKG-04 generates the Windows resource object
   (`.syso`) from the kit's `.ico` and verifies it. T-F2-50 builds the first Windows executable,
   and with it closes REQ-PKG-005 by checking the executable's icon group. Go links a `.syso`
   named `*_windows_<arch>.syso` into Windows builds only, so the file can sit in
   `cmd/umbral-desktop/` without affecting the Linux build. **REQ-PKG-005 stays open until
   T-F2-50.**
3. **The generator is go-winres, pinned and outside the module.** `packaging/desktop/syso.sh`
   (`task desktop:syso`) runs `github.com/tc-hib/go-winres@v0.3.3 simply`. `go run pkg@version`
   builds the tool from its own `go.mod` and never touches Umbral's, and an empty `GOFLAGS`
   keeps an inherited `-mod=…` from applying. It writes `rsrc_windows_amd64.syso` and
   `rsrc_windows_arm64.syso`. The Tech Lead chose this over vendoring `tc-hib/winres` or a
   hand-written COFF writer on 2026-10-31. Auto mode refuses downloaded code, so a developer
   runs the script; CI's `desktop` job runs it with `--check`, which regenerates into a
   temporary directory and compares.
4. **What the resource holds.** It holds the kit's eight images as they are, and go-winres's
   default manifest: `asInvoker` and system DPI awareness. The icon group lists the entries
   largest first, the generator's order; Windows picks an entry by size, so the order is not
   checked. T-F2-50 may replace the manifest when it builds the executable.
5. **No Taskfile override is needed.** The task's title and its archived plan
   (`changes/_archive/2026-09-visual-identity/tasks.md`) meant replacing a Wails project's
   `generate:icons` step, so that the build copies `wails-build/` instead of regenerating
   icons. This build has neither: `packaging/desktop/build.sh` runs `go build` alone, and
   `syso.sh` takes the kit's `.ico` as it is, so REQ-PKG-006 holds by construction. The dock
   screenshot that plan asked for is step 1.3 of `docs/qa/f2-desktop.md`.

## Tests

- `scripts/desktop_smoke.sh` (CI `desktop` job): the live window's `WM_CLASS`. A build with
  `AppID = "io.github.ecrespo.Mutant"` showed the mutant class.
- `TestTheWindowsResourceCarriesTheKitIcon_REQ_PKG_005` (`packaging/desktop`): the kit's `.ico`
  has the eight sizes. Each `.syso` is for its own machine (`0x8664`, `0xaa64`) and has one
  icon group listing each entry once. The test walks the `.rsrc` directory, and each group
  entry's RT_ICON id holds that entry's image byte for byte. It reddened on:
  - a flipped byte in the hand-drawn 16 px image;
  - a changed size in a group entry;
  - the two architectures swapped;
  - two group entries' ids swapped.

## Folded into

- Tech 1.81 §9.3: the Windows resource and the window id.
- `specs/tasks/umbral-f2-tasks.md`: T-PKG-04 done; REQ-PKG-005's executable check added to
  T-F2-50; the traceability rows.

## Review

spec-guardian, 2026-10-31: FIX FIRST. At 4ffa8e4, `task arch` failed: `packaging/desktop` was
in no component. e08fc82 makes it a leaf component on the standard library. Also applied:
- the Wayland `app_id` measured and given a QA step (decision 1);
- the Taskfile override and dock screenshot accounted for (decision 5);
- the test now checks the machine and the RT_ICON ids;
- REQ-PKG-005 stated open until T-F2-50;
- the smoke checks for `xwininfo` up front and polls for as long as the window lives;
- `syso.sh` rejects unknown arguments;
- the `desktop` job's checkout keeps no credentials;
- the `GOFLAGS` sentence corrected, and `readICO` bounds-checked.
Supply chain: the tool is pinned, its content fixed by the checksum database, and the
workflow's token is read-only.
