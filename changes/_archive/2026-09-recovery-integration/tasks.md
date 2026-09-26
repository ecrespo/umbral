# Tasks — delta `2026-09-recovery-integration`

### [x] 2026-09-26 T-RIV-01 · Put the recovery verdict in Data Model §6
- **What:** Data Model §6 step 1 settles every `pending` integration after marking alive
  sessions exited — `osc133` with blocks, `none` without; Data Model 1.8. The F0 task list gains `T-F0-22`, its matrix entry
  and the plan's row.
- **REQ:** REQ-BLK-003
- **Files:** `specs/data-model/umbral-schema.md`, `specs/tasks/umbral-f0-tasks.md`,
  `specs/plans/umbral-mvp-plan.md`
- **Done:** `python3 tools/sdd_check.py` green.

### [x] 2026-09-26 T-F0-22 · A restart settles the integration verdict a crash interrupted
> The implementation. Lives in `specs/tasks/umbral-f0-tasks.md`; repeated here so the delta
> carries its own record of what it asked for.
- **What:** `store.Recover` settles every `pending` row — `osc133` when it has blocks, `none`
  otherwise — in the same transaction as steps 1 and 2, and reports how many; `umbrald` logs
  the count.
- **REQ:** REQ-BLK-003
- **Done:** `TestRecoverySettlesAPendingIntegration_REQ_BLK_003` green, and a `kill -9` inside
  a session's window followed by a restart reads the old row back as `exited|none`.
