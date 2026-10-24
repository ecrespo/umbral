# Delta — F2a closes on what US-F2-001 measured

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-24 — decided by E. Crespo ("Cerremos la F2a y comencemos con el hardening"), accepting the closing criterion the implementing agent proposed the same day; applied to specs/ and archived` |
| **Date** | 2026-10-24 |
| **Task** | none — a plan change, no code |
| **Raised by** | The implementing agent, once every F2a task was merged (T-F2-00 … T-F2-12, PRs #33 … #47) |

## Evidence

- **Tasks:** every F2a task is merged in `develop` (`docs/checkpoints/2026-10-24-f2a-complete.md`).
- **US-F2-001:** run live four times (`docs/reports/usf2-2026-10-24.md`).
  - The daemon's properties held in every run:
    - the main copy was untouched until the merges;
    - no thread's change reached another thread's worktree;
    - every turn had a trace;
    - every hosted write and command was decided by the policy engine.
  - Run 4 counted 0 `egress_log` rows and found no process left behind.
  - Run 1 had every task done within its files and three clean merges.
  - Runs 2 to 4 failed on the models' work: a hosted rename that did nothing, a `tool_error`, and
    an approved edit of `go.mod`.
- **The gap:** PRD F2 §8 closes F2a "when US-F2-001 passes" and gives no pass rate. Delta
  `2026-10-usf2-live` left this as its question 3.

## Decision

1. **What "US-F2-001 passes" means for F2a.** Two things must both hold:
   - **The daemon's properties held in every run measured.** These are:
     - the main copy untouched before the merges;
     - no change of one thread in another's worktree;
     - every turn with a trace;
     - every hosted side effect decided by the policy engine;
     - from run 4, no `egress_log` row.
   - **At least one run completed every task.** That run had all three threads finished, each
     within its task's files, and every merge clean.

   The quality of each model's work is reported, not required in every run. It depends on the
   model, not on Umbral. The four runs of 2026-10-24 meet this, so **F2a is closed**.
2. **The F2a budgets stay measurements.** NFR-F2-01…03 are asserted by tests under the `live`
   tag and run by `e2e/usf2/run.sh`. They are not `task perf` gates. Each run builds a 1 GiB
   repository, and §7 asks no gate of them. This can be revisited when F2d's daemon upgrade
   touches the same paths.
3. **What carries over.**
   - These stay open in T-HRD-02:
     - the hosted agent's start-up traffic and an offline manifest;
     - hosted reads across sibling worktrees.
   - The stale "nothing below is implemented yet" in the PRD's status line is replaced.
   - F2b … F2f are untouched and start from their own deltas.

## Changes

- `specs/prd/umbral-f2.md` 0.8: the status line, §8's F2a row, and a change-history row.
- `changes/_archive/2026-10-usf2-live/delta-spec.md`: question 3 is marked answered here.
- `specs/tasks/umbral-f2-tasks.md`: an execution-log row.
- `AGENTS.md`: the known status.
- `docs/checkpoints/2026-10-24-f2a-closed.md`.
