# CHECKPOINT — Phase 0 closed (verified against the filesystem)

> 2026-09-11 · branch `feat/T-PKG-visual-identity` · closes the delta-folding phase.

## Phase 0 tasks

| Task | State | Where it landed |
|---|---|---|
| T-FIX-01 … T-FIX-05 | done | `changes/_archive/2026-09-analyze-fixes/` |
| T-APIF0-01, T-APIF0-02 | done | `changes/_archive/2026-09-api-f0-decisions/` |
| T-PKG-01, T-PKG-02 | done | `changes/_archive/2026-09-visual-identity/` |
| T-PKG-03, T-PKG-04, T-PKG-05 | **blocked, not pending** | T-PKG-03 needs a package to build (hardening); the other two need the F2 desktop client, and T-PKG-05 needs Icon Composer on a Mac |

`changes/` holds no pending delta. All three are archived.

## Done criteria, each verified by running

| Criterion | Command | Result |
|---|---|---|
| SDD coverage and DDL | `python3 tools/sdd_check.py` | exit 0 · 69 REQs (64 MUST) · 64/64 with a task and a matrix row · 37 tasks · 0 uncited |
| Diagrams | `node tools/mermaid_check.mjs` | 14/14 valid |
| Branding kit | `task icons` | checksums verified, kit regenerated and compared byte for byte, `.desktop` validated |
| Whole gate | `task ci` | exit 0 |

The REQ count dropped from 73 to 69 on purpose: the four `REQ-PKG-*` requirements that
describe the F2 desktop client left the MVP's coverage set.

## Findings closed in this phase

| Finding | How |
|---|---|
| A-01 CRITICAL | `threads` moved to migration 0001; regression test verified by reverting the fix |
| A-02 … A-07 | folded into the PRD, API, Tech Design and Data Model |
| A-12 | REQ-PKG-004, 005, 007 and 008 went to `specs/prd/umbral-f2-desktop.md` instead of becoming MVP MUSTs that no MVP task could close |
| A-13 | T-F0-09 file globs narrowed |
| A-15 | `CHECKSUMS.sha256` regenerated after the `.desktop` change |
| A-16 | `changes/2026-09-identidad-icono/`, the superseded Spanish copy, deleted |

Still open and attached to the task that first needs each: A-08 (reference machine,
T-F0-13), A-09 (entropy and rules precedence, T-F1-04 and T-F1-11), A-10 (SQLite write
failure, T-F1-13), A-11 (`fetch_url` limits, T-F1-09), A-14 (glossary).

## What the branding work actually verifies

`scripts/icons_check.sh` does three things, and the second is the one REQ-PKG-006 asks for:

1. every artifact matches `CHECKSUMS.sha256`;
2. regenerating the kit from `tools/build.py` reproduces those artifacts **byte for byte**;
3. the `.desktop` file validates and carries the release 0.1 launcher.

Step 1 alone would pass a source edit committed together with its new checksum. The kit does
reproduce: 55 of the 56 artifacts were already byte-identical, and the `.desktop` file was the
only intended change. Verified to have teeth by flipping one byte of the 48 px PNG: exit 1.

## Honest status

- `desktop-file-validate` exits 0 but emits one **hint**: `Categories` lists more than one
  main category. It is pre-existing, it is not an error, and REQ-PKG-002 asks only that the
  file pass without errors. Left for whoever revisits the kit's categories.
- The regeneration step **degrades to a warning** when `cairosvg` and `Pillow` are missing, so
  a contributor without them still gets checks 1 and 3. CI installs both, so the full check
  always runs there. On a machine without them, `task icons` is weaker than it looks.
- `specs/prd/umbral-f2-desktop.md` is a **holding place, not a PRD**. It exists so four
  requirements are not lost between the archived delta and an F2 PRD that does not exist yet.
- The `icons` CI job, like every other job, has **never run on real GitHub**.
- Phase 0 closing does not mean the specs are approved. They are still `DRAFT`; the Analyze
  presents findings and approves nothing.
