# Delta — `worktree.*`, threads in their worktree, `umb worktree`

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-22 by the implementing agent under the Tech Lead's standing F2 instruction; folded into API 1.44 (§2, §3, §4, §5.19, §5.36, §5.41, §9), Tech 1.59 (§5.2, §5.3, §5.3b) and F2 PRD 0.5 (REQ-WT-001's path)` |
| **Date** | 2026-10-22 |
| **Task** | T-F2-03 |
| **REQ** | REQ-WT-002, REQ-WT-007, and the wire of REQ-WT-001, 003, 004, 005, 006 and 008 |

## Decisions the PRD and the task file left open

1. **Where a turn runs.** The thread's `cwd` is never rewritten. At the start of each turn the
   runtime asks for the thread's active worktree, through a port of its own
   (`agents/ports.Worktrees`) that `cmd/umbrald` adapts to the engine. With one, the turn's write
   root is the worktree, and its cwd is the worktree's directory matching the thread's `cwd`,
   or the worktree's top when the worktree lacks that directory (an ignored one). Attachments
   resolve there too. Once the worktree is merged, discarded or missing, the next turn is back
   in the thread's own directory with no write to the thread.
2. **The thread's PTY follows its cwd.** `run_command` reuses the thread's shell only while it
   is asked for the cwd it was started in. A different cwd closes that shell and starts a new
   one there. Otherwise a shell started before the worktree would keep running the agent's
   commands in the main copy.
3. **No worktree change under a running turn.** Decision 19 of `2026-10-worktree-engine` asked
   this of a merge. It is extended to `create`, `discard` and a `close_group` that discards: each
   would move or remove the directory a running turn writes in.
   - Each runs under the thread's send lock and answers `CONFLICT` `running` while a turn runs.
     A send holds that lock until its turn is written as running, so no turn can start meanwhile.
   - "A turn runs" is read from the store, as a send reads it: the thread is `running` or
     `awaiting_approval`. A turn's end is written before `thread.turn_finished` is published, so a
     merge sent the moment a wait returns is not refused.
   - The lock is held for the whole change, up to `commit_timeout` for a merge. A `thread.send`
     to that thread waits for it meanwhile, and so does its connection's next request.
   - `running` joins `data.reason`'s `CONFLICT` words.
   - A queued turn (T-F2-04) must count as running here once it exists.
4. **`thread.create` with `worktree: true` keeps the thread when git refuses.** The worktree's
   row points at the thread, so the thread is written first. When git refuses the worktree — a
   `VALIDATION_ERROR` reason of `worktree.create`: not a repository, bare, too old, no commit —
   the thread keeps working without isolation (REQ-WT-008, and REQ-ACP-001's "REQ-WT-008 refused
   it"). The answer is the thread with `worktree_id: null` and `worktree_refused: {reason,
   message}`; `umb thread create` says so on stderr. Any other failure of the worktree deletes the
   thread and is the answer. A failure to record `worktree_id` after the worktree was made is the
   answer too, and leaves both; the worktree is listed under the thread and can be discarded.
5. **`threads.worktree_id`** records the thread's latest worktree, whatever its state. The
   `worktrees` row decides whether a turn uses it. `Thread` gains `worktree_id` and
   `workspace_id` on the wire.
6. **`workspace`** must name an open workspace (`VALIDATION_ERROR` otherwise). The daemon cannot
   know which pane a request came from, so REQ-ORC-003's default — "the one owning the creating
   pane" — is the client's: `umb thread create` sends `UMBRAL_WORKSPACE_ID`, which every pane
   carries, unless `--workspace` says otherwise.
7. **`close_group`'s shape.** A group is `workspace_id` (its threads) or `thread_ids`, exactly
   one. The answer lists every worktree of those threads, in every state, oldest first, each with
   `changes` (`changed` or `clean`) when it was active and `discarded`. `changes` is read as the
   diff being non-empty, untracked files included.
   - Only active worktrees named in `discard` are discarded, and that needs `confirm: true`.
   - A named worktree that is not active is reported, not an error; one outside the group is
     `VALIDATION_ERROR` `not_in_group`.
   - Every check is made before the first discard. The discards run inside the running-turn
     guards of all their threads, each entered once, in thread-id order: no turn can start on one
     thread after another's worktree is gone, and two groups never lock in opposite orders.
   - A discard git fails part way returns its error; the ones before it stand, each complete.
8. **Error data.** `data.reason` on `VALIDATION_ERROR` and `CONFLICT`; `data.paths` for
   `conflict`; `data.output` for `commit_failed`. The fields are absent elsewhere. The
   VALIDATION words are the engine's request refusals; the CONFLICT words are decision 5's five
   plus `changed`, `exists`, `not_active` and `running`.
9. **Who may call what.** `umb` gets `list`, `get`, `diff`, `merge` and `discard`: the review and
   landing a user does at a shell. `worktree.create` on an existing thread and `close_group` stay
   with the interactive clients, which own threads and workspaces. `umb thread create --worktree`
   may ask for a worktree at a thread's birth: it isolates a thread before its first turn and
   moves nothing a turn is using.
   - `umb worktree merge|discard` join the destructive patterns as `umb_worktree_change`, so an
     agent running commands cannot land or drop its own work without the user's review.
   - `umb worktree diff`, `merge` and `discard` wait for the daemon's answer or Ctrl-C, not
     `umb`'s 10-second call bound: a merge may take `commit_timeout` with hooks and signing, and
     giving up early would report a failure for a commit that lands.
   - `umb worktree merge` reads the diff, prints it on stderr, and asks `y` on a terminal (or
     takes `--yes`). It then merges with the diff's `revision`, printing only the commit on stdout.
   - `umb worktree discard` asks the same way.
   - Repository content reaches the terminal escaped.
10. **The inbound port.** `worktrees/ports.Worktrees` is what `api` serves, so `api` holds the
    module's ports with its domain, as Tech §5.2 requires. The request and result types moved
    from the engine's package to its domain.
11. **Where worktrees live.** `worktrees/` beside the database, which is
    `$XDG_DATA_HOME/umbral/worktrees` for the default database. This is the skill store's rule,
    and REQ-WT-001's wording says so (F2 PRD 0.5): a daemon given `--db` keeps its worktrees with
    its data. At start, every active worktree gone from disk is marked missing, before the
    runtime can run a turn in one (REQ-WT-006). A git that cannot be run is logged and does not
    stop the daemon.
12. **`policy.explain` explains a thread in its worktree.** API §5.36 promises the engine and
    directories a turn decides with. A thread with an active worktree lends the worktree's cwd and
    write root, computed by the one function the runtime uses (`agents.TurnDirs`).
13. **A turn's cwd stays in its worktree.** The mapped directory is used only while its real path
    is inside the worktree: a link the agent made there leading out leaves the turn at the
    worktree's top. The `run_command` policy cwd is the turn's, not where a reused shell has `cd`'d;
    T-F2-05's "Exec whose cwd is inside" must account for that.
14. **A git failure at turn start stops the turn `storage_error`.** The turn cannot know where it
    runs, and falling back to the main copy would write outside the worktree, so it fails closed.
    `storage_error` already means "the turn could not record or read what it depends on"; no new
    stop reason is added in F2.
15. **A merge that landed is a merge.** Once the commit is on the origin branch and the row says
    `merged`, a worktree directory or branch that cannot be removed is logged and left over; the
    answer carries the commit. A failure to record `merged` itself is the answer, and the commit
    is logged: the row stays active over landed work, and a second merge finds nothing to land.

## Tests

- `TestAThreadWritesOnlyInItsWorktree_REQ_WT_002`, against a real daemon and git: a relative
  write lands in the worktree; an absolute write to the main copy asks even in `auto-edit`; the
  diff and a merge with its revision land it on main.
- `TestCloseGroupNeverDropsAnUnmergedChange_REQ_WT_007`, `TestCloseGroupReportsAMissingWorktree_REQ_WT_007`,
  `TestCloseGroupDiscardsOnlyThroughItsGuard_REQ_WT_007` and `TestListMarksAMissingWorktree_REQ_WT_006`,
  against git.
- `TestATurnRunsInItsThreadsWorktree_REQ_WT_002`, `TestATurnsCwdStaysInItsWorktree_REQ_WT_002`,
  `TestAThreadKeepsWorkingWhenItsWorktreeIsRefused_REQ_WT_008`,
  `TestAWorktreeCannotChangeUnderARunningTurn_REQ_WT_004` and
  `TestAWorktreeChangesAsSoonAsItsTurnEnds_REQ_WT_004`, in the runtime.
- `TestAThreadWhoseWorktreeIsRefusedKeepsWorking_REQ_WT_008` against a real daemon, and
  `TestThreadCreateSaysWhenItsWorktreeWasRefused_REQ_WT_008` in `umb`.
- `TestExplainUsesTheThreadsWorktree_REQ_WT_002`; the daemon test above also checks that
  `policy.explain` agrees with the turn.
- `TestCloseGroupHoldsEveryGuardAcrossItsDiscards_REQ_WT_007` and
  `TestAMergeThatLandedIsAMergeAfterAFailedCleanUp_REQ_WT_004`, against git.
- `TestDestructiveAlwaysAsk_REQ_SEC_005` gains `umb worktree merge|discard`.
- `TestUmbWorktreeWaitsForALongMerge_REQ_WT_004`.
- `TestAThreadsPTYFollowsItsCwd_REQ_WT_002`, against a real PTY.
- `TestWorktreeMethodsOverTheWire_REQ_WT_004`, `TestCloseGroupOverTheWire_REQ_WT_007` and
  `TestUmbMayReviewButNotCreateWorktrees_REQ_WT_004`.
- `TestUmbWorktreeMergesExactlyTheDiffItShowed_REQ_WT_004` and
  `TestUmbWorktreeExplainsARefusal_REQ_WT_004`.
