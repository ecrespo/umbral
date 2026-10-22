# Delta — migration 0007: worktrees and the F2 columns

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-22 by the implementing agent under the Tech Lead's standing F2 instruction ("puedes autoaprobar los deltas"); folded into Data Model 1.24` |
| **Date** | 2026-10-22 |
| **Task** | T-F2-01 |
| **REQ** | REQ-WT-001, REQ-ORC-002, REQ-ORC-003, REQ-ORC-004, REQ-ACP-004, REQ-ACP-005, REQ-ACP-007 |

## Decisions the task file left open

1. **Each new column carries its `CHECK` in the `ALTER`.**
   - Data Model §2.5 said SQLite cannot add a `CHECK` in `ADD COLUMN`. That is wrong: SQLite
     accepts one, and since 3.37 it tests it against the rows already there.
   - `TestMigration0007Constraints_REQ_WT_001` measures it on the driver Umbral ships. The test
     goes red when a `CHECK` is removed.
   - The writers still validate first, so a client gets a `VALIDATION_ERROR` rather than a
     constraint failure.
2. **`worktrees.thread_id` has no cascade.**
   - A worktree holds unmerged work, so a thread with any worktree row cannot be deleted.
     History rows count too: merged, discarded and missing rows are kept.
   - The retention job's `idleThread` skips such a thread rather than trying the delete and
     rolling it back on every run.
   - `TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001` goes red if a cascade is added.
3. **One active worktree per thread, and per path.**
   - Two partial unique indexes enforce this.
   - Merged, discarded and missing rows stay as history.
4. **`threads.worktree_id` and `threads.workspace_id` are `ON DELETE SET NULL`.**
   - The retention job deletes closed workspaces (§4). A thread outlives its workspace and
     loses only the grouping.
5. **A queued turn is recovered by its flag, not by its state.**
   - §6 step 3 stops every thread with `queued = 1`, whatever its `state`, and clears the flag.
   - `TestAQueuedTurnIsRecoveredStopped_REQ_ORC_002` holds the rule.
6. **An ACP `tool_calls` row belongs to the turn's assistant message**, as PRD F2 REQ-ACP-004
   says, so `message_id` stays `NOT NULL`.

## Folded into

Data Model 1.24:
- §2.4g `worktrees`;
- the migration 0007 blocks in §2.2, §2.5 and §2.7;
- the correction in §2.5;
- the 0007 row in §5;
- §6 step 3.
