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

## Prefixed identifiers

[[internal/store/ids.go#NewID]] generates type-prefixed ULIDs and lives in `store` because the `CHECK` constraints enforcing the prefixes sit in the migrations next door.

Timestamps are UTC epoch milliseconds and costs are `int64` micro-USD everywhere, including across the API.
