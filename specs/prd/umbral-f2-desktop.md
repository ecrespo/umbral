# Umbral F2 — Desktop client: scoped requirements

## Metadata

| Field | Value |
|---|---|
| **Author** | Ernesto Crespo · assisted draft |
| **Status** | `DRAFT` (stub) |
| **Version** | 0.1 |
| **Date** | 2026-09-11 |
| **Scope** | F2 only. This is **not** part of the MVP and nothing here blocks release 0.1. |

---

## 1. Why this file exists

The visual identity delta defined eight `REQ-PKG-*` requirements. Four of them describe the
Wails desktop client and the macOS bundle, which the MVP does not build. Finding **A-12** of
the 2026-09-11 Analyze is explicit: folded into the MVP PRD as-is, they would have been MUST
requirements that no MVP task could ever close.

They live here instead, out of the MVP's coverage count, until the full F2 PRD is written and
absorbs them.

`specs/prd/umbral-mvp.md` §6.10 keeps REQ-PKG-001, 002, 003 and 006, which the TUI release can
and does satisfy.

## 2. Requirements deferred to F2

- **REQ-PKG-004** · MUST · event — WHEN the desktop client creates its window, THE SYSTEM SHALL set the Wayland `app_id` and the X11 `WM_CLASS` to `io.github.ecrespo.Umbral`.
- **REQ-PKG-005** · MUST · ubiquitous — THE SYSTEM SHALL embed in the Windows executable an `.ico` with entries at 16, 20, 24, 32, 40, 48, 64 and 256 px, where the 16 and 24 px entries come from the hand-drawn sources.
- **REQ-PKG-007** · MUST · ubiquitous — THE SYSTEM SHALL, on macOS, include an `Assets.car` compiled from `appicon.icon` (Icon Composer) with `CFBundleIconName = appicon`, and a fallback `icons.icns` for macOS ≤ 15.
- **REQ-PKG-008** · SHOULD · ubiquitous — THE SYSTEM SHOULD provide an icon variant for nightly builds, distinguishable at 48 px.

## 3. Tasks that will close them

`T-PKG-04` (Wails icons, app id and Taskfile override) and `T-PKG-05` (macOS `.icon` and
`Assets.car`) from the archived delta. Both need the F2 desktop client, and T-PKG-05
additionally needs Icon Composer on a Mac, which no CI runner provides.

The kit already carries what they will consume: `wails-build/`, `macos/icon-composer-layers/`
and `windows/umbral.ico`.

## 4. When this file goes away

It is a holding place, not a PRD. The F2 PRD absorbs these four requirements and this file is
deleted in the same change.

## Change History

| Version | Date | Changes |
|---|---|---|
| 0.1 | 2026-09-11 | Created while folding `2026-09-visual-identity`, to honour Analyze finding A-12 |
