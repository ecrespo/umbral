# Tasks — Delta 2026-09-analyze-fixes

> Delta: `changes/2026-09-analyze-fixes/delta-spec.md` · Generated: 2026-09-11
> Runs **before T-F0-02**. It only edits specs and the checker; no product code.

## Tasks

### [x] 2026-09-11 T-FIX-01 · Fold A-01 and A-07 into the Data Model
- **What:**
  - move `threads` to migration 0001 (§5);
  - write the FTS triggers (§2.4);
  - update `tools/sdd_check.py` so the 0001 simulation includes `threads`;
  - adjust the text of T-F0-02 and T-F1-01.
- **REQ:** REQ-TERM-001, REQ-BLK-006
- **Depends on:** approval of this delta
- **Done:** `python3 tools/sdd_check.py` without CRITICAL findings; a manual `sqlite3` test that INSERTs into `sessions` + `blocks` and runs an FTS `MATCH` returns the expected row.

### [x] 2026-09-11 T-FIX-02 · Fold A-02 (VT appendix)
- **What:** add §8.1 to the Tech Design and adjust T-F0-07.
- **REQ:** REQ-TERM-002
- **Depends on:** —
- **Done:** the Tech Design contains VT-01…VT-22; T-F0-07 references them.

### [x] 2026-09-11 T-FIX-03 · Fold A-03 and A-04 (new REQs)
- **What:**
  - add REQ-SEC-008 and REQ-AGT-015 to the PRD;
  - add `client_msg_id` to the API and the Data Model;
  - update T-F1-02 and T-F1-13 and their matrices.
- **REQ:** REQ-SEC-008, REQ-AGT-015
- **Depends on:** T-FIX-01
- **Done:** `python3 tools/sdd_check.py` shows 73 REQs defined, with the 2 new ones covered by a task and a matrix.

### [x] 2026-09-11 T-FIX-04 · Fold A-05 and A-06 (API)
- **What:** `owner_thread_id` in `Session`; rename `env_keyring_refs` → `env_refs`.
- **REQ:** REQ-AGT-003, REQ-MCP-001
- **Depends on:** —
- **Done:** `rg env_keyring_refs specs/` returns nothing; `Session` includes the new field.

### [x] 2026-09-11 T-FIX-05 · Re-run the Analyze
- **What:** run `/sdd-analyze` and produce a new report that marks A-01…A-07 as resolved or persistent; archive this delta in `changes/_archive/`.
- **REQ:** REQ-TERM-001, REQ-TERM-002, REQ-SEC-008, REQ-AGT-015
- **Depends on:** T-FIX-01 … T-FIX-04
- **Done:** new report with the verdict `READY TO IMPLEMENT` or only MEDIUM/LOW findings open.

## Traceability matrix

| REQ | Tasks | Verification |
|---|---|---|
| REQ-SEC-008 | T-FIX-03 → T-F1-02 | TestKeyringUnavailableDisablesProviders_REQ_SEC_008 |
| REQ-AGT-015 | T-FIX-03 → T-F1-13 | TestSendIdempotentByClientMsgID_REQ_AGT_015 |

## Execution log

| Date | Tasks | Result | Notes |
|---|---|---|---|
| 2026-09-11 | T-FIX-01 … T-FIX-05 | folded into `specs/` | A-01 closed: `threads` moves to migration 0001 (Data Model §5.1) and `sdd_check.py` now simulates 0001 with an INSERT plus an FTS `MATCH`. A-02: Tech Design §8.1 with VT-01…VT-22. A-03/A-04: REQ-SEC-008 and REQ-AGT-015 in the PRD, `client_msg_id` in the API and in `messages`. A-05/A-06: `owner_thread_id` in `Session`, `env_keyring_refs` → `env_refs`. A-07: the three `blocks_fts` triggers written out. A-13 folded in passing (T-F0-09 file globs narrowed). `python3 tools/sdd_check.py` exits 0. |
