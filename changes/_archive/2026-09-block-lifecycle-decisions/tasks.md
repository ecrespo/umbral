# Tasks — block lifecycle decisions

Conventions are those of `specs/tasks/umbral-f0-tasks.md`: `[ ]` pending, `[~]` in progress,
`[!]` blocked, `[x] YYYY-MM-DD` done.

### [x] 2026-09-11 T-BLD-01 · Fold the delta into the specs
- **What:** apply the five MODIFIED sections of `delta-spec.md` to the four spec files, bump
  each file's version and change history, then archive this folder under `changes/_archive/`.
- **REQ:** REQ-BLK-002, REQ-BLK-003, REQ-BLK-004, REQ-BLK-007
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `specs/data-model/umbral-schema.md`,
  `specs/prd/umbral-mvp.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** approval of this delta
- **Done:** `python3 tools/sdd_check.py` exits 0 and `node tools/mermaid_check.mjs` accepts
  the amended §7 diagram.
- **Result:** API Spec v1.3 (§4 gloss, §7 two new edges), Data Model v1.2 (§2.2 flag covers
  both caps, §2.3 lists what the chunks drop), PRD v1.3 (REQ-BLK-003 late promotion and the
  one-way rule), Tech Design v1.3 (§3.2 records `klauspost/compress/zstd`). Both checkers
  green: 14/14 diagrams, 64/64 MUST REQs with a task.

## Traceability matrix

| REQ | Task | Test |
|---|---|---|
| REQ-BLK-002 | T-BLD-01 | TestRecorderAbandonsASupersededBlock_REQ_BLK_002 (already green) |
| REQ-BLK-003 | T-BLD-01 | TestLateIntegrationPromotesTheSession_REQ_BLK_003 (already green) |
| REQ-BLK-004 | T-BLD-01 | TestScannerBracketsTheAlternateScreen_REQ_BLK_004 (already green) |
| REQ-BLK-007 | T-BLD-01 | TestPlainTextCapsALineWithNoNewline_REQ_BLK_007 (already green) |
