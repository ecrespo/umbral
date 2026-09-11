# Tasks — block lifecycle decisions

Conventions are those of `specs/tasks/umbral-f0-tasks.md`: `[ ]` pending, `[~]` in progress,
`[!]` blocked, `[x] YYYY-MM-DD` done.

### [ ] T-BLD-01 · Fold the delta into the specs
- **What:** apply the five MODIFIED sections of `delta-spec.md` to the four spec files, bump
  each file's version and change history, then archive this folder under `changes/_archive/`.
- **REQ:** REQ-BLK-002, REQ-BLK-003, REQ-BLK-004, REQ-BLK-007
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `specs/data-model/umbral-schema.md`,
  `specs/prd/umbral-mvp.md`, `specs/technical/umbral-architecture.md`
- **Depends on:** approval of this delta
- **Done:** `python3 tools/sdd_check.py` exits 0 and `node tools/mermaid_check.mjs` accepts
  the amended §7 diagram.

## Traceability matrix

| REQ | Task | Test |
|---|---|---|
| REQ-BLK-002 | T-BLD-01 | TestRecorderAbandonsASupersededBlock_REQ_BLK_002 (already green) |
| REQ-BLK-003 | T-BLD-01 | TestLateIntegrationPromotesTheSession_REQ_BLK_003 (already green) |
| REQ-BLK-004 | T-BLD-01 | TestScannerBracketsTheAlternateScreen_REQ_BLK_004 (already green) |
| REQ-BLK-007 | T-BLD-01 | TestPlainTextCapsALineWithNoNewline_REQ_BLK_007 (already green) |
