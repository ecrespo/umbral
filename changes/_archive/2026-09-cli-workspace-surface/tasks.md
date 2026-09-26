# Tasks — delta `2026-09-cli-workspace-surface`

### [x] 2026-09-21 T-CWS-01 · Put the workspace CLI in the specification package
- **What:** PRD §6.8 gains REQ-CLI-005 and REQ-CLI-006; the F0 task list gains `T-F0-20` and
  its two rows in the traceability matrix; the plan's F0 table gains the task and its fourth
  exit criterion stops reading "the feature does not exist".
- **REQ:** REQ-CLI-005, REQ-CLI-006
- **Files:** `specs/prd/umbral-mvp.md`, `specs/tasks/umbral-f0-tasks.md`,
  `specs/plans/umbral-mvp-plan.md`
- **Done:** `python3 tools/sdd_check.py` green with both REQs carrying a task and no HIGH
  finding; `T-F0-20` present in the F0 task list, the traceability matrix and the plan's F0
  table; PRD at 1.11 with its changelog row.
- **Result:** applied on 2026-09-21. The checker is what settles *when* this runs: it reads
  every non-archived `changes/*/delta-spec.md`, so a pending delta that defines a MUST while
  `specs/` stays untouched makes `task specs` report `REQ-CLI-005 (MUST) missing from the
  traceability matrix`. Applying the text and leaving the delta `PENDING APPROVAL` is also
  what `2026-09-cli-surface` and `2026-09-structure-migration` did while they waited. The
  ratification, and `T-F0-20` itself, are still a human's.
- **Note:** the API Specification is **not** touched. No method changes, so `task schema`
  compares the same 33 methods before and after.

### [ ] T-F0-20 · `umb workspace`, `tab`, `pane` and `layout`
> The implementation. Lives in `specs/tasks/umbral-f0-tasks.md`;
> repeated here so the delta carries its own record of what it asked for.
- **What:**
  - `umb workspace create|list|focus|rename|close`, `umb tab create|list|focus|rename|close`,
    `umb pane split|list|get|focus|rename|close`, `umb layout export|apply`, each one call to
    the method of the same name;
  - objects addressed positionally by `w<n>`, `w<n>:t<m>` and `w<n>:p<m>`;
  - `--json` and the REQ-CLI-004 exit codes, as every other `umb` command;
  - `layout apply --from <file|->`.
- **REQ:** REQ-CLI-005, REQ-CLI-006
- **Files:** `cmd/umb/workspace.go`, `cmd/umb/layout.go`, `cmd/umb/main.go`,
  `cmd/umb/*_test.go`, `scripts/cli_roundtrip.sh`
- **Depends on:** T-F0-11, T-F0-14, T-F0-15
- **Done:** the five tests of the delta's Verification section green, and
  `scripts/cli_roundtrip.sh` passing against a real daemon — which is F0 exit criterion 4,
  performed rather than argued.
- **Out of scope, deliberately:** `pane.move` (decision 3).
