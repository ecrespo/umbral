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
   The Wayland `app_id` is not observable under xvfb. GTK sets it from the same application id, so
   it is covered by that option and by the manual QA of release 0.1.
2. **REQ-PKG-005 is split at the executable.** T-PKG-04 generates the Windows resource object
   (`.syso`) from the kit's `.ico` and verifies it. T-F2-50 builds the first Windows executable,
   and with it closes REQ-PKG-005 by checking the executable's icon group. Go links a `.syso`
   named `*_windows_<arch>.syso` into Windows builds only, so the file can sit in
   `cmd/umbral-desktop/` without affecting the Linux build.
3. **The generator is go-winres, pinned and outside the module.** `packaging/desktop/syso.sh`
   (`task desktop:syso`) runs `github.com/tc-hib/go-winres@v0.3.3 simply` with `GOFLAGS=`
   unset, so neither `go.mod` gains a dependency, and writes `rsrc_windows_amd64.syso` and
   `rsrc_windows_arm64.syso`. The Tech Lead chose this over vendoring `tc-hib/winres` or a
   hand-written COFF writer on 2026-10-31. Auto mode refuses downloaded code, so a developer
   runs the script; CI's `desktop` job runs it with `--check`, which regenerates into a
   temporary directory and compares.
4. **What the resource holds.** It holds the kit's eight images as they are, and go-winres's
   default manifest: `asInvoker` and system DPI awareness. The icon group lists the entries
   largest first, the generator's order; Windows picks an entry by size, so the order is not
   checked. T-F2-50 may replace the manifest when it builds the executable.

## Tests

- `scripts/desktop_smoke.sh` (CI `desktop` job): the live window's `WM_CLASS`. A build with
  `AppID = "io.github.ecrespo.Mutant"` showed the mutant class.
- `TestTheWindowsResourceCarriesTheKitIcon_REQ_PKG_005` (`packaging/desktop`): the kit's
  `.ico` has the eight sizes, every image is in both `.syso` files byte for byte, and the icon
  group lists each entry once. It reddened on a flipped byte in the hand-drawn 16 px image and
  on a changed size in a group entry.

## Folded into

- Tech 1.81 §9.3: the Windows resource and the window id.
- `specs/tasks/umbral-f2-tasks.md`: T-PKG-04 done; REQ-PKG-005's executable check added to
  T-F2-50; the traceability rows.

## Review

Pending spec-guardian.
