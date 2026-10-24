# CHECKPOINT — F2a, every task done

> 2026-10-24 · scope: the work done on 2026-10-24 after the Tech Lead's review of the third batch,
> under the instruction to finish F2a without check-ins. Verified against `git log origin/develop`
> (#45 and #46 merged), the tasks files, a `task ci` run on this PR's own tree and the live runs'
> logs, not against notes. T-F2-12 is committed with this checkpoint.

## Verdict

All twelve F2a tasks (T-F2-00 … T-F2-12) are marked `[x]` in `specs/tasks/umbral-f2-tasks.md`.
The fourth batch has three tasks. Each had a spec-guardian review, its findings fixed before the
commit, and a green `task ci`:

| Task | PR | Delta (self-approved) | What it does |
|---|---|---|---|
| T-F2-08 | #45 | `2026-10-acp-mediation` | A hosted agent's permissions, `fs/*` and `terminal/create` are decided by the running turn's policy, performed by Umbral and recorded |
| T-F2-09 | #46 | `2026-10-acp-cancel-traces` | A cancel kills a hosted turn's terminal commands first. Hosted calls and requests become `tool.acp:*` spans. `umb thread create --agent` and the panel disclose `unobserved` |
| T-F2-12 | this PR | `2026-10-usf2-live` | US-F2-001 run live four times; the three F2a budgets measured (`docs/reports/usf2-2026-10-24.md`) |

F2a is **not closed**. Closing it is the Tech Lead's call, after reviewing this batch and the
report.

## US-F2-001, as measured

The scenario: two `gpt-oss:20b` threads and OpenCode 1.18.33 over ACP, all on the local Ollama,
each thread in its own worktree.

- **Held in all four runs (the daemon's properties):**
  - the main copy stayed untouched until the merges;
  - no thread's change appeared in another thread's worktree;
  - every turn had a trace;
  - every hosted write and command was decided by the policy engine.
- **Run 1:** passed every criterion.
- **Run 2:** failed on the models' work. The hosted rename did nothing, and fix ended
  `tool_error` although its test passed.
- **Run 3:** failed because fix also edited `go.mod`, an edit the simulated user approved.
- **Run 4:** the hardened test, after spec-guardian's review. It counted 0 `egress_log` rows and
  found no process left after the daemon stopped. It failed because, as in run 2, OpenCode
  searched twice and ended its turn without renaming anything.
- **Budgets:**

  | Budget | Measured | Target |
  |---|---|---|
  | `worktree.create` on 1 GiB, p95 | 1.24 s | 2 s |
  | Each additional idle thread | 0.16 MiB | 15 MiB |
  | ACP mediation, p95 | 1.1 ms | 20 ms |

## For the Tech Lead

1. **PRD F2 §9 was changed (0.7).** The scenario is not offline. With the empty cache its isolated
   home gives it, OpenCode fetches its provider package from npm and a model catalogue at start.
   This is the agent's own traffic, and it is disclosed as `unobserved`. Delta `2026-10-usf2-live`.
2. **Q-F2-01 was reworded the same way.** OpenCode stays the reference agent.
3. **Two questions were added to T-HRD-02:**
   - should the manifest make a hosted agent offline?
   - should a hosted agent see the sibling worktrees that git shows it?
4. **What "US-F2-001 passes" means is open (delta question 3).** With `gpt-oss:20b` the tasks
   were done strictly in scope in one run of four. Runs 1, 3 and 4 met §4's rule (nothing outside
   each worktree), but in run 4 the hosted rename did nothing; only run 1 met §9's (only the
   task's files) with every task done.
5. **One flake was seen once on macOS:** `TestTheUsersGlobalAgentConfigIsNotRead_REQ_ACP_009` (the
   agent exited with status 2). It did not recur on the rerun.

## What remains of F2

- F2a: the review, then the phase close.
- Later slices: T-F2-20 … T-F2-61. They are broken down when each starts.
- Hardening (release 0.1): T-REL-01, T-REL-02 and T-HRD-01 … T-HRD-03.
