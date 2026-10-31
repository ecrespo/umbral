# Delta — the desktop window's id and the Windows icon resource

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the Tech Lead ("si, apruebo") for the scope below; in progress |
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

## Open

- **The `.syso` generator.** The plan was `github.com/tc-hib/go-winres`, the pure-Go tool behind
  Wails' own `generate syso`, run pinned through `go run …@v0.3.3`. Auto mode refused to run
  downloaded code, so the choice of tool is pending the Tech Lead.
