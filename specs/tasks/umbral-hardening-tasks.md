# Tasks — Umbral hardening 0.1 (packaging and identity)

> Source specs: `specs/prd/umbral-mvp.md` §6.14 · `specs/plans/umbral-mvp-plan.md` (hardening phase)
> Folded from `changes/_archive/2026-09-visual-identity/`. REQ-PKG-004, REQ-PKG-005, REQ-PKG-007
> and REQ-PKG-008 live in `specs/prd/umbral-f2.md` §6.8 (finding A-12; the F2 PRD absorbed the
> holding file `umbral-f2-desktop.md` on 2026-10-22), with their tasks T-PKG-04 and T-PKG-05 in
> `umbral-f2-tasks.md`.

## Conventions for this file

Same as `umbral-f0-tasks.md`. Packaging is verified with commands rather than Go tests, which is why
its matrix lists commands.

## Tasks

### [x] 2026-09-11 T-PKG-01 · Incorporate the kit and pin checksums
- **What:**
  - the kit lives in `assets/branding/umbral-icons/`;
  - `CHECKSUMS.sha256` covers every binary of the kit;
  - CI job `icons` that rebuilds with `tools/build.py` and compares.
- **REQ:** REQ-PKG-001, REQ-PKG-006
- **Files:** `assets/branding/umbral-icons/**`, `scripts/icons_check.sh`, `.github/workflows/ci.yml`, `Taskfile.yml`
- **Depends on:** —
- **Done:** the `icons` job is green; changing one pixel of a PNG turns it red.
- **Result:** `scripts/icons_check.sh` does three things rather than one: verify the
  checksums, regenerate with `build.py` and compare, then validate the `.desktop` file. The
  regeneration step is what REQ-PKG-006 actually asks for, because checking the checksums
  alone would pass a source edit committed together with its new checksum. Verified by
  flipping one byte of the 48 px PNG: exit 1. The kit reproduces byte for byte from source,
  so 55 of the 56 artifacts were already correct; only the `.desktop` file changed, in
  T-PKG-02. The regeneration step degrades to a warning when `cairosvg` and `Pillow` are
  missing, so a contributor without them still gets the checksum and `.desktop` checks; CI
  installs both, so the full check always runs there.

### [x] 2026-09-11 T-PKG-02 · `.desktop` file for release 0.1
- **What:** set `Exec=umbral-tui` and `Terminal=true`, validate it in CI, and regenerate
  `CHECKSUMS.sha256` in the same commit because the `.desktop` file is covered by it (finding A-15).
- **REQ:** REQ-PKG-002, REQ-PKG-003
- **Files:** `assets/branding/umbral-icons/linux/share/applications/**`, `assets/branding/umbral-icons/CHECKSUMS.sha256`
- **Depends on:** T-PKG-01
- **Done:** `desktop-file-validate` without errors and the `icons` job still green after the change.
- **Result:** the change went into the `DESKTOP` template in `build.py`, not into the
  generated file, so the next regeneration keeps it. `desktop-file-validate` exits 0; it
  emits one *hint* about `Categories` listing more than one main category, which is
  pre-existing, is not an error, and is left for whoever revisits the kit's categories.
  `scripts/icons_check.sh` additionally asserts the four lines REQ-PKG-002 and REQ-PKG-003
  name, because the validator does not know which binary release 0.1 ships. Checksums
  regenerated, closing A-15.

### [ ] T-PKG-03 · Installation in the `.deb`
- **What:** ship the hicolor tree and the `.desktop` file in the package; postinst triggers refresh
  `gtk-update-icon-cache` and `update-desktop-database`.
- **REQ:** REQ-PKG-001, REQ-PKG-002
- **Files:** `packaging/deb/**`
- **Depends on:** T-PKG-02
- **Done:** `dpkg -c umbral_*.deb` lists the 11 icon files and the `.desktop` file; smoke test in an
  Ubuntu 26.04 container.

### [ ] T-REL-01 · A week of the TUI as the main terminal, before release 0.1
- **What:** the TUI used for one week as the main terminal without blocking regressions —
  F0's third exit criterion, moved here by delta `2026-09-defer-tui-week` so the week is spent
  on the TUI that ships, agent panel included. A human's to perform; no code.
- **REQ:** Art. 9 — a release gate set by the plan (delta `2026-09-defer-tui-week`); no REQ.
- **Files:** `docs/qa/` (the record of the week, beside `docs/qa/f0-tui.md`)
- **Depends on:** the F1 TUI work (T-F1-20)
- **Done:** seven days of the TUI as the daily driver with no blocking regression, recorded
  where `docs/qa/f0-tui.md` was. Release 0.1 does not ship without it.

### [ ] T-REL-02 · Walk `docs/qa/f1-tui.md`, before release 0.1
- **What:** the human walk of the agent panel that closes `T-F1-20`'s Done line, moved here by
  delta `2026-10-f1-close` when F1 closed. It includes the extensions view T-F1-37 added: the
  `mcp:` and `skills:` rows, a server going `unavailable`, and a turn's row of MCP tools and
  loaded skills. A human's to perform; no code unless a step fails.
- **REQ:** REQ-TUI-001, REQ-TUI-002, REQ-TUI-003, REQ-TUI-004
- **Files:** `docs/qa/f1-tui.md`
- **Depends on:** T-F1-20, T-F1-37. **Goes before T-REL-01.**
- **Done:** every step of `docs/qa/f1-tui.md` walked on a real daemon with no step failing, and
  the walk recorded in the file. Release 0.1 does not ship without it.

### [ ] T-HRD-01 · The project's rule-signing key
- **What:** the Tech Lead creates the project's signing key under
  `docs/runbooks/rule-signing.md` §4.1, and its fingerprint becomes the trust seed T-F1-30
  ships empty (delta `2026-10-f1-close`).
- **REQ:** REQ-SEC-011, REQ-SEC-013, Art. 5 (signed rule material)
- **Files:** `internal/security/adapters/rules/trustseed/`, `docs/runbooks/rule-signing.md`
- **Depends on:** T-F1-30
- **Done:** a bundle signed with the key and served locally is accepted by a daemon built with the
  seed, and one signed with any other key is refused and recorded in `egress_log`.

### [ ] T-HRD-02 · The questions F1's ratified deltas left open
- **What:** answer each of the seven by a delta, or accept it in writing in the delta that raised
  it (delta `2026-10-f1-close`):
  - `2026-10-skill-catalog`: forms of a command the normaliser does not see (`env -S`, a word
    built by substitution, a raw JSON-RPC client); `umb rules key add|rotate`, `rollback` and
    `reset` not being destructive; `skill_load` serving the stored copy without checking its
    digest;
  - `2026-10-umb-mcp-panel`: the catalog's descriptions reaching the system prompt without
    tainting the turn; `umb skill`/`umb mcp` from the agent's own PTY guarded only by patterns,
    not refused by the daemon; `umb mcp remove` not being destructive; which `env_refs` a `cli`
    client may set.
- **REQ:** REQ-SEC-005, REQ-SKL-005, REQ-CLI-008
- **Files:** `changes/<YYYY-MM>-<slug>/` per decision; code only where a decision asks for it
- **Depends on:** —
- **Done:** each question names its answer and where it was decided; `task ci` green on any code
  that answer required.

### [ ] T-HRD-03 · Three defects known when F1 closed
- **What** (delta `2026-10-f1-close`):
  - `fetch_url` follows four redirects where Tech §5.3 allows five: `CheckRedirect` refuses at
    `len(via) >= fetchMaxHops`, and `via` already holds the first request;
  - `TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005` timed out on the macOS runner after
    five seconds reading the reply (PR #26, run 39779926649's first attempt, 2026-10-22), and
    passed on a rerun. The `REQ_AGT_007` cancel flake first listed here was fixed under this
    task by delta `2026-10-agent-pty-settle` (PR #27) before F1 closed;
  - API §5.37 numbers two sections (`api.schema` and `skill.*`).
- **REQ:** REQ-AGT-018, REQ-API-005, REQ-API-004
- **Files:** `internal/tools/adapters/builtin/fetch.go`, its test, the macOS flake's test file,
  `specs/api/umbral-daemon-api-v1.md`
- **Depends on:** —
- **Done:** a test that follows exactly five redirects and refuses the sixth, seen red first; the
  flake's cause found and fixed, with the test green over repeated runs on macOS; one §5.37, with
  every reference to the renumbered section updated and `task schema` green.

## Traceability matrix (hardening)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-PKG-001 | T-PKG-01, T-PKG-03 | `icons` job, `dpkg -c` |
| REQ-PKG-002 | T-PKG-02, T-PKG-03 | `desktop-file-validate` |
| REQ-PKG-003 | T-PKG-02 | `.desktop` review in CI |
| REQ-PKG-006 | T-PKG-01 | the `icons` job regenerates the kit with `build.py` and compares byte for byte; checksums alone would pass a source edit committed with its new checksum |
| REQ-TUI-001, 002, 003, 004 | T-REL-02 (walk) | `docs/qa/f1-tui.md`; the harness tests are F1's |
| REQ-SEC-011, REQ-SEC-013 | T-HRD-01 (the key) | a bundle signed with the project key accepted, any other refused |
| REQ-SEC-005, REQ-SKL-005, REQ-CLI-008 | T-HRD-02 (decisions) | per decision |
| REQ-AGT-018, REQ-API-005, REQ-API-004 | T-HRD-03 | a five-redirect test; the macOS flake over repeated runs; `task schema` |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-PKG-01, T-PKG-02 | done | Folded from `changes/_archive/2026-09-visual-identity/`. The `icons` CI job verifies the checksums, regenerates the kit from source with `tools/build.py` and compares byte for byte, then validates the `.desktop` file. The launcher targets `umbral-tui` with `Terminal=true`, since release 0.1 ships no desktop client. Recorded in the F0 tasks file until the hardening file existed; moved here on 2026-09-20 with the rest of the packaging work. |
| 2026-10-18 | T-REL-02, T-HRD-01, T-HRD-02, T-HRD-03 | added | Delta `2026-10-f1-close`: F1 closed, and what it left moved here — `T-F1-20`'s QA walk as a release gate, the rule-signing key, the seven questions the nine ratified deltas left open, and three known defects. |
| 2026-10-22 | T-HRD-03 | revised | Its `REQ_AGT_007` item — a late interrupt eating the first byte of the agent's next command — fixed by delta `2026-10-agent-pty-settle` (PR #27, `TestALateInterruptDoesNotEatTheNextCommand_REQ_AGT_007`) and replaced by the `REQ_API_005` macOS read timeout seen on PR #26 (delta `2026-10-f1-close`, revised). The task stays open for the other three items. |
