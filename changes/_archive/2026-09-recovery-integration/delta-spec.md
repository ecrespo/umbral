# Delta — a restart settles the integration verdict a crash interrupted

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — commissioned and approved by E. Crespo in the same instruction ("crea un nuevo delta para resolver la recuperación tras un crash … impleméntalos"); drafted, applied and archived by the implementing session` |
| **Date** | 2026-09-26 |
| **Task** | T-F0-22 |
| **Raised by** | The `spec-guardian` review of T-F0-21 (finding 2), 2026-09-26 |

## Evidence

**T-F0-21 settles the verdict on the live exit path only.** Since `202eda2`,
`Service.finish` writes `integration: none` for a session that exits while still `pending`,
before its state flips. A daemon that dies has no `finish` to run: `kill -9`, a panic, a power
cut. On the next start, Data Model §6 step 1 turns every `alive` session into `exited` — and
says nothing about `integration`, so `store.Recover` leaves it as it was:

```sql
UPDATE sessions SET state = 'exited', exit_code = NULL, exited_at = ? WHERE state = 'alive'
```

A session that was still inside its five-second window when the daemon died therefore comes
back `exited|pending`, and stays so forever: nothing will ever judge it again, because no
process is behind it. That is the state REQ-BLK-003 forbids and T-F0-21 removed from the live
path — reintroduced by a crash.

**Rows written before T-F0-21 carry the same lie.** Every session that exited inside the
window on a daemon older than `202eda2` was persisted `exited|pending` (measured on
2026-09-21: `ses_01M33B3GKWBH5MFVDTWT6A0SEW|exited|pending|0`). Those rows exist in real
databases now, and no code path ever revisits them.

**Why it is a delta and not a fix.** §6 is the specification of recovery, step by step, and
it is silent on `integration`. Adding a write to `Recover` that §6 does not list would be code
diverging from the spec silently, which the working rules forbid.

## Decisions

**1. After step 1, every `pending` row is settled: `osc133` if it has blocks, `none`
otherwise.** At the moment `Recover` runs, the daemon has not started a single session: every
row describes a process that is gone, and a `pending` verdict on it means the judge never ran.
Most sessions that emitted a marker are already `osc133` and untouched. But not all: the drain
writes a block row *before* the `osc133` verdict the same marker produces, and that verdict's
write can be cut off by the crash or fail and be only logged. Such a session has blocks and a
`pending` row, and calling it `none` would be the disagreement REQ-BLK-003 names — a session
saying it has no integration while its blocks are recorded. So the blocks decide: a session
with any is `osc133`; the rest emitted no marker, and REQ-BLK-003's verdict for that is
`none`. Found by the `spec-guardian` review of T-F0-22.

**2. Not only the sessions step 1 just exited.** The rule is `integration = 'pending'` over
the whole table, not `WHERE state = 'alive'`, so the rows written before T-F0-21 are repaired
by the first restart of a daemon that carries this change. No data migration: the fix is a
recovery rule, which runs on every start, and Art. 6's forward-only migrations are not
touched.

**3. In the same transaction as steps 1 and 2.** A crash mid-recovery must not leave a
session `exited` with its verdict still pending, for the same reason §6 already keeps
sessions and blocks together.

**4. Counted.** `RecoveryReport` gains `IntegrationSettled`, logged beside
`sessions_recovered` and `blocks_abandoned`, so an operator can see that a restart repaired
verdicts — and a first start after upgrading says how many old rows it fixed.

## Specification changes

- **Data Model §6, step 1** becomes: `sessions.state = 'alive'` → `exited`, with
  `exit_code = NULL` and `exited_at = now`; then every `sessions.integration = 'pending'` →
  `osc133` when the session has blocks and `none` otherwise, because no session is alive at
  this point and a verdict left pending belongs to a process that can no longer be judged
  (REQ-BLK-003).
- **Data Model changelog:** 1.8.

The PRD is **not** touched: REQ-BLK-003 already says what should happen; this delta says where
recovery does it.

## Verification

- `TestRecoverySettlesAPendingIntegration_REQ_BLK_003` — an `alive|pending` row and an
  `exited|pending` row both come back `none`, a `pending` row with a block comes back
  `osc133`; an `osc133` row and an existing `none` row are untouched; the report counts three,
  and a second `Recover` in the same test settles nothing.
- Against a real daemon: a pane running `sh -c 'sleep 600'`, the daemon killed with `kill -9`
  inside the window, restarted, and the old session's row read back as `exited|none`.

## Phase

**F0**, as `T-F0-22`: it completes REQ-BLK-003, an F0 MUST, on the one path T-F0-21 could not
reach.
