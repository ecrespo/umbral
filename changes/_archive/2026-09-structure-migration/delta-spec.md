# Delta — which migration creates the workspace, tab and pane tables

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-09-20 — folded into specs/` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-14 (blocked on this), T-F1-01 |
| **Approved by** | Ernesto Crespo |
| **Raised by** | reconciliation: the orchestration surface was authored against a pre-implementation baseline |

## Evidence

Data Model 1.4 introduced `workspaces`, `tabs`, `panes` and `pane_aliases` and placed them in
**migration 0001**, alongside the terminal tables. That text was written on 2026-09-20 from a
baseline dated 2026-09-11 09:20, which predates the F0 implementation commits of the same
day. By then migration 0001 had already been written, applied and shipped:

```
$ ls internal/store/migrations/
0001_terminal.sql   0002_block_index.sql
$ grep -c 'CREATE TABLE' internal/store/migrations/0001_terminal.sql
5          # schema_migrations, threads, sessions, blocks, block_chunks — no structure tables
```

Constitution Art. 6 makes migrations forward-only, and `internal/store` records only the
version a database reached. Editing 0001 would therefore change nothing for any database that
already ran it: those databases would come up without a `workspaces` table and with nothing to
report the divergence. This is the same reasoning that gave `idx_blocks_started` migration
`0002` of its own in delta `2026-09-block-query-performance`, approved on 2026-09-11.

The spec as written was also unimplementable: T-F0-14's "Files" line named
`internal/store/migrations/0001_terminal.sql`, a file the task is forbidden to modify.

## MODIFIED

### specs/data-model/umbral-schema.md → §2.4b, §2.4c, §2.4d, §2.4e and §5

- **Before:** the structure tables are created in migration `0001`; the F1 agent tables,
  `pane_state_reports`, `pane_metadata`, `pane_history`, `trust_keys` and `rule_bundles` in
  migration `0002`.
- **After:** four migrations, listed in a table in §5:

  | Migration | Phase | Objects |
  |---|---|---|
  | `0001_terminal.sql` | F0 | terminal tables, `threads`, `blocks_fts` and its triggers |
  | `0002_block_index.sql` | F0 | `idx_blocks_started` |
  | `0003_structure.sql` | F0 | `workspaces`, `tabs`, `panes`, `pane_aliases` |
  | `0004_agent.sql` | F1 | the agent, model, audit, MCP and integration tables |

- **Reason:** nothing already applied is edited (Art. 6). 0001 and 0002 are applied on every
  developer machine; 0003 and 0004 are not written yet, so they are free to take the content.

### specs/tasks/umbral-f0-tasks.md → T-F0-14

- **Before:** "`workspaces`, `tabs`, `panes` and `pane_aliases` tables in migration 0001";
  Files: `internal/store/migrations/0001_terminal.sql`.
- **After:** migration `0003_structure.sql`, with the forward-only reason stated inline.
- **Reason:** the task could not be executed as written without violating Art. 6.

### specs/tasks/umbral-f1-tasks.md → T-F1-01

- **Before:** "Migration 0002 (agent, models, audit, MCP)", Data Model tables §2.5 to §2.13.
- **After:** "Migration 0004", tables §2.4c to §2.13, with explicit notes that `threads`
  (§2.5, finding A-01) and the structure tables (§2.4b) are created elsewhere.
- **Reason:** the numbering moves with the Data Model, and the §2.5 range would have made
  T-F1-01 re-create `threads`, reopening finding A-01.

### specs/data-model/umbral-schema.md → §2.5 `threads`

- **Before:** the `threads` DDL block, which §5 assigns to migration `0001`, carried
  `attention_state` and `seen_at` (REQ-AGT-016, REQ-NTF-002).
- **After:** the two columns move out of that block into explicit `ALTER TABLE` statements
  under migration `0004`, and T-F1-24 names `0004_agent.sql` in its Files line.
- **Reason:** the same Art. 6 rule, found one table later. `internal/store/migrations/0001_terminal.sql`
  is applied and has neither column, so the spec described a table that does not exist on any
  database. `attention_state` carries a `DEFAULT` because `ALTER TABLE … ADD COLUMN` must fill
  the existing rows, and its `CHECK` is stated in prose because SQLite cannot add one in an
  `ALTER`.

### specs/plans/umbral-mvp-plan.md → F1 task table

- **Before:** "T-F1-01 · Migration 0002".
- **After:** "T-F1-01 · Migration 0004".

## NOT MODIFIED

The table definitions themselves are untouched: same columns, same constraints, same indexes.
This delta moves them between files, nothing else. `protocol_version` and the API contract are
unaffected, since no client can observe which migration created a table.

## Verification

- `python3 tools/sdd_check.py` exits 0 with the DDL assembled from the new migration list.
- `TestMigrationsAreContiguousFrom0001` in `internal/store` still passes: 0003 and 0004 do not
  exist yet, and the runner requires contiguity only over the files present.
- T-F0-14 is unblocked: it creates a new file rather than editing an applied one.
