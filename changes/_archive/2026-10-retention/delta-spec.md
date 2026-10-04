# Delta — the retention job, the crash test and the user guide

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-04` — approved in advance by E. Crespo, to be reviewed afterwards; amended after a spec-guardian review. Folded into PRD 1.19, API 1.30, Data Model 1.17, Tech Design 1.39, Plan 1.12 and the F0 and F1 tasks files |
| **Date** | 2026-10-04 |
| **Task** | T-F1-22 |
| **Raised by** | T-F1-22: Data Model §4, REQ-AGT-011 and Art. 6, which leave these points open |

## Evidence

- **Data Model §4** lists windows and "a daily maintenance job". It does not say:
  - where the configurable windows are set, or the name of the closed-structure key ("yes");
  - what age a block is measured from;
  - what is left of a block whose output is purged;
  - what an ephemeral thread takes with it. `blocks.thread_id` and `sessions.owner_thread_id`
    reference `threads` with no cascade, and `tool_calls.block_id` references `blocks`;
  - whether a running thread or block may be purged;
  - when the job runs, and how it avoids holding the writer.
- **REQ-WS-002** makes `w<n>` identifiers unique "while the object exists".
  `treestore.nextWorkspaceOrdinal` allocates from the highest number present. Purging a closed
  workspace therefore frees its number. The code's comment already counts on this.
- **T-F1-22's Done** names `TestCrashRecovery_REQ_AGT_011` without saying what it crashes.
- **The user guide** had no home: `docs/user/` did not exist.
- **REQ-TERM-005** keeps "the session's blocks in the database". The blocks CHECK
  `origin = 'user' OR thread_id IS NOT NULL` makes an agent block impossible to keep once its
  thread is gone.
- **API §5.17 and the Block type** say nothing about output that retention purged.

## Decisions

1. **`[retention]` in `config.toml`.**
   - The keys are `raw_output_days`, `plain_output_days`, `closed_structure_days` (a name chosen
     here) and `audit_days`.
   - Each is whole days, from 1 to 3650. The defaults are §4's: 30, 180, 30 and 365.
   - A value out of range stops the daemon and names the key, like any malformed setting.
   - The subset parser strips quotes, so `"30"` reads as 30, as `max_message_bytes = "8MiB"`
     already does.
   - The keys are not live: a change needs a restart.
   - The 24 h for ephemeral threads and for pane metadata stay fixed, as §4 says.
2. **When the job runs.**
   - Once when the daemon starts, beside it rather than before it serves. A first run after a
     long pause can have much to purge, and no client waits for it.
   - Then every 24 h, until the daemon stops. A clean stop waits for a run in progress before
     closing the database.
   - A failed run is logged, and the next run tries again.
   - Each run ends with `PRAGMA optimize` and the FTS `optimize`.
   - The log line `retention applied` gives each step's count.
3. **Batches.**
   - Every step deletes or updates at most 500 rows per transaction, and loops until nothing is
     left. Raw chunks are counted as rows, not blocks, since one block holds up to 256 of them.
     `TestRetentionBatchesAreBoundedInRows` holds the bound.
   - So a purge never holds the writer for long while a turn is persisting.
   - Two exceptions are §4's own:
     - The FTS `optimize` merges the whole index in one write.
     - The two block steps scan `blocks` for their age, because `coalesce(ended_at,
       started_at)` uses no index.

     Both are bounded by the size of the history, not by what is purged.
4. **Raw output.**
   - Only closed blocks (`finished` or `abandoned`) lose their chunks.
   - A block's age is counted from `ended_at`, or from `started_at` when no end was recorded.
   - The block's row, its `output_bytes` and its transcript stay.
   - `block.get` with `include: raw` then answers an empty output, and after decision 5 so does
     `include: plain`.
   - `output_bytes` still says how much there was. It is how a client tells a purged output
     from one that never existed: `output_bytes > 0` with nothing returned.
   - A running block is never touched, however old.
5. **Transcripts.** Past the window, `output_plain` becomes NULL. The FTS update trigger takes
   it out of the index. The command stays, and search still finds it.
6. **Ephemeral threads.**
   - A thread with `ephemeral = 1` is purged 24 h after its `updated_at`, and only when all of
     these hold:
     - it is neither `running` nor `awaiting_approval`;
     - it owns no `alive` session;
     - it has no open block.
   - The conditions are checked again inside the thread's own transaction. A thread that stopped
     qualifying after the listing is skipped: a send landed, or a session came alive. So is one
     whose transaction fails, and the job goes on.
   - One transaction per thread, in the order the foreign keys allow:
     1. its approvals;
     2. its tool calls;
     3. its closed agent blocks;
     4. its exited sessions get `owner_thread_id = NULL`. The sessions stay, as terminal history;
     5. the thread. Messages and thread rules cascade.
   - Its `usage` rows stay as audit with a NULL thread (`ON DELETE SET NULL`). Its `egress_log`
     rows keep the thread id as plain text.
   - **An exception to REQ-TERM-005.** The thread's agent blocks go with it, because the schema
     cannot hold an agent block without its thread. Their session stays. `umb ai` threads are
     read-only and have none; only an ephemeral thread created through the API with tools can.
   - A thread whose PTY session is alive is kept. That session lives until the daemon restarts,
     so such a thread is purged after the next restart, not at 24 h.
7. **Closed structure.**
   - Workspaces, then tabs, then panes whose `closed_at` is past the window are deleted. What
     they hold cascades, `pane_aliases` included.
   - A purged number may be given to a later object. REQ-WS-002's uniqueness holds while the
     object exists.
   - A moved pane's old identifier is never reissued, even in a workspace that reused a purged
     number. The pane allocator counts `pane_aliases` (REQ-WS-007), and
     `TestAPurgedWorkspaceNumberNeverReissuesAnAlias_REQ_WS_007` proves it.
     `treestore.go`'s comment, which said closed rows were never deleted, is corrected.
8. **Pane metadata** is deleted past its own `expires_at`, or 24 h after `updated_at` when that
   comes first. A restart already clears it (§6 step 8).
9. **Audit.** `egress_log` and `usage` rows whose `created_at` is past `audit_days` are deleted.
10. **The crash test** (`TestCrashRecovery_REQ_AGT_011`, `cmd/umbrald/crash_test.go`). A real
    daemon is killed with SIGKILL twice and restarted on the same directories each time:
    - **Crash 1:** a turn waits for a `write_file` approval the client was shown. Afterwards the
      user's message, the streamed text and the tool call are in the store, the thread is
      `stopped`, the approval is `expired`, and the file was not written.
    - **Crash 2:** the model is mid-stream. A new `scriptedHold` line keeps the scripted Ollama
      generating. Afterwards every delta the client received is in the assistant's message.
    - **After both:** `thread.send` resumes the thread, and the model's request carries the
      whole history.
    - It proves REQ-AGT-011's "persist before announcing" by its observable consequence: what
      the client saw always survives the crash.
11. **The user guide.**
    - `docs/user/providers.md`:
      - `models.toml` and its classes;
      - keyring references and the `env:` fallback;
      - local versus remote, and `offline`;
      - the presets;
      - what `umb status` and the catalog say;
      - how to restart after an edit.
    - `docs/user/data-retention.md` describes this job for users.
12. **Found while writing the guide.** `router.max_cost_usd_per_thread` is parsed and converted
    to micro-USD, but nothing enforces it.
    - API §3 describes `BUDGET_EXCEEDED` as a "token/cost budget", but no REQ requires the cap.
    - The Tech Lead asked on 2026-10-04 for it to be enforced. That is its own change, delta
      `2026-10-cost-cap`.
    - Until then the guide says it is not enforced.
13. **Data Model §4's "manual deletion" of non-ephemeral threads has no method.** There is no
    `thread.delete`. The guide says threads are kept indefinitely, with no way to delete one yet.
14. **Test names.** Only the tests that verify a REQ carry one:
    - `TestRetentionClearsOldTranscripts_REQ_BLK_006`: the transcript leaves search;
    - `TestAPurgedWorkspaceNumberNeverReissuesAnAlias_REQ_WS_007`;
    - `TestCrashRecovery_REQ_AGT_011`.

    The rest test Data Model §4, which no REQ states, and carry no id.

## What changes in the specs on ratification

- **API:** §5.17 and §4's Block. Purged output reads empty, and `output_bytes` keeps the size
  (decision 4). This is additive within protocol v1.
- **PRD REQ-TERM-005:** a sentence naming the retention exception for an ephemeral thread's agent
  blocks (decision 6).
- **Data Model §4:**
  - the `closed_structure_days` key;
  - the setting names prefixed with `retention.` as they are written in `config.toml`;
  - decisions 2–9 as notes under the table.
- **Tech Design §5.1, Configuration:** `[retention]` and its four keys (decision 1).
- **`specs/tasks/umbral-f1-tasks.md`:**
  - T-F1-22 is marked done.
  - Its Files gain `cmd/umbrald/retention.go`, `internal/config/settings.go` and
    `cmd/umbrald/crash_test.go`.
  - The matrix:
    - REQ-AGT-011 already names `TestCrashRecovery_REQ_AGT_011`;
    - REQ-BLK-006 adds `TestRetentionClearsOldTranscripts_REQ_BLK_006`;
    - REQ-WS-007 adds `TestAPurgedWorkspaceNumberNeverReissuesAnAlias_REQ_WS_007`.
- **Plan:** T-F1-22 ✅.
