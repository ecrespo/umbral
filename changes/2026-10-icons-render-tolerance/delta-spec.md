# Delta — the branding kit's regeneration compares rendered images within a tolerance

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-30` by the implementing agent under the Tech Lead's standing F2 instruction, pending the Tech Lead's review |
| **Date** | 2026-10-30 |
| **Task** | T-PKG-01 (its verification), found on PR #65 (T-F2-31) |
| **REQ** | REQ-PKG-006 |

## What changed

The `icons` CI job went red on PR #65, which touches no icon. On the runner image
`ubuntu-26.04` 20261025.179.1 the kit's images of 512 px and over — seven files — regenerate
differently from the committed ones. With cairosvg 2.9.0 and Pillow 12.0.0 pinned, the kit
still reproduces byte for byte on the developer's machine and in a clean Ubuntu 26.04
container with the same libcairo 1.18.4. cairo rasterises through pixman, whose SIMD paths
round differently from one CPU to another. A re-run on the same image failed the same way.

## Decision

Step 2 of `scripts/icons_check.sh` still compares every artifact byte for byte first. A
rendered image that differs — a PNG, or an `.icns` or `.ico` whose entries are PNGs — passes
when it has the same entries in the same order, the same sizes and mode, and no channel of
any pixel moves by more than **2** (`scripts/icons_compare.py`); anything else must be
identical. The largest difference is printed for each such file. Step 1, the checksums
REQ-PKG-006 names, is unchanged, so the files that ship are still exactly the committed ones.

The runner measured at most 1. The check still bites: a pixel moved by 4 fails, and one
colour changed in `tools/svgs.py`, committed with its new checksums, fails with differences
of 213 to 255.

## Folded into

- Tech §9.3, Verification, and its 1.79 changelog row.
- `specs/tasks/umbral-hardening-tasks.md`: the REQ-PKG-006 row.
