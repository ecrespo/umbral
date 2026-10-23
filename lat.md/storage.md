# Storage

Decisions behind `internal/store`, which owns the SQLite schema; the schema is the contract with every module.

## Pragmas in the DSN

[[internal/store/store.go#Open]] passes pragmas in the DSN and reads `foreign_keys` and `journal_mode` back instead of trusting the driver.

`database/sql` pools connections, so a pragma issued once after connecting applies to one connection only.

## Threads table in migration 0001

Migration 0001 creates `threads`, an F1 table, because `sessions.owner_thread_id` and `blocks.thread_id` hold foreign keys against it.

SQLite rejects every insert into a table whose foreign key names a missing table, even with a NULL value (Analyze finding A-01). [[internal/store/migrate_test.go#TestMigration0001InsertsWithForeignKeysOn]] is the regression test; moving `threads` breaks all of F0.

## Forward-only migrations

An applied migration file is never edited (constitution Art. 6), which is why the agent subdomain is `0005` and not the number older drafts name.

For the same reason the bundle bytes that signed rule updates need arrive in `0006_rule_state` as an `ALTER TABLE` on `rule_bundles`, not as an edit to `0005`.

## Orchestration columns without a rebuild

Migration `0007_orchestration` adds the F2 worktree table and columns to `threads`, `tool_calls` and `blocks` by `ALTER`, because the runner's one transaction with foreign keys on cannot rebuild `threads`.

Each added column carries its own `CHECK`. SQLite accepts a `CHECK` in `ADD COLUMN` and tests it against the existing rows, which [[internal/store/migration0007_test.go#TestMigration0007Constraints_REQ_WT_001]] proves.

`full-auto` is a `full_auto` flag beside `mode` rather than a new mode value, so clearing it restores the thread's own mode.

Migration `0008_full_auto` keeps the flag from outliving its worktree: its triggers clear `full_auto` in the write that moves a worktree out of `active` or takes it from its thread ([[internal/store/migration0008_test.go#TestALeftWorktreeClearsFullAuto_REQ_ORC_004]]), and [[internal/agents/adapters/threadstore/threadstore.go#Store#UpdateThread]] sets it only while the worktree row is `active`.

`threads.workspace_id` is written at creation and must name a workspace; `threads.worktree_id` records the thread's latest worktree whatever its state, set by [[internal/agents/adapters/threadstore/threadstore.go#Store#SetWorktree]]. Whether a turn runs in it is the `worktrees` row's state, not this column.

`worktrees.thread_id` has no cascade. A thread holding a worktree cannot be deleted, not even by the retention job ([[internal/store/migration0007_test.go#TestRetentionKeepsAThreadWithAWorktree_REQ_WT_001]]).

[[internal/store/recovery.go#Store#Recover]] stops a thread whose turn was held `queued`, the same way as a running one.

## Prefixed identifiers

[[internal/store/ids.go#NewID]] generates type-prefixed ULIDs and lives in `store` because the `CHECK` constraints enforcing the prefixes sit in the migrations next door. A worktree's prefix is [[internal/store/ids.go#PrefixWorktree]] (`wtr`), since `wt` was already a wait's.

Timestamps are UTC epoch milliseconds and costs are `int64` micro-USD everywhere, including across the API.
