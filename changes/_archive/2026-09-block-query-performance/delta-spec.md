# Delta — what `block.list` and `block.search` order by, and the index that makes it possible

| Field | Value |
|---|---|
| **Status** | `APPROVED — folded into specs/ on 2026-09-11` |
| **Date** | 2026-09-11 |
| **Task** | T-F0-10 |
| **Raised by** | measurement: `BenchmarkBlockSearch100k_REQ_BLK_006` missed its budget by six times |

## Evidence

REQ-BLK-006 budgets 200 ms p95 for `block.search` over 100,000 blocks. Measured on the
reference machine (AMD Ryzen AI 9 HX PRO 370, Linux, `modernc.org/sqlite`), against a
100,000-block corpus of realistic commands and build output:

| Query | p95 |
|---|---|
| `block.list`, ordered by `started_at DESC` as §3 specifies | **141 ms** |
| the same, with an index on `(started_at DESC, id DESC)` | **0.18 ms** |
| `block.search`, ordered by `started_at DESC` | **139 ms** |
| the same, with the index above | **167 ms** |
| `block.search`, ordered by the FTS5 rowid descending | **0.45 ms** |
| `block.search`, ordered by FTS5 `rank` | **159 ms** |

`EXPLAIN QUERY PLAN` says why. Ordering a search by `started_at` puts a `USE TEMP B-TREE FOR
ORDER BY` over the whole match set, so a term matching half the history sorts fifty thousand
rows to return fifty. Ordering by the FTS5 rowid lets SQLite walk the full-text index
backwards and stop at the limit, which is why it is three hundred times faster. An index on
`started_at` does not help the search, because the rows arrive from the full-text index in
rowid order and have to be re-sorted whatever indexes exist.

## MODIFIED

### specs/data-model/umbral-schema.md → §2.2 `blocks` indexes, and §5.1 migration 0001

- **Before:** three indexes, all of them prefixed by `session_id`, `thread_id` or `state`.
- **After:** a fourth, in migration 0001 with the others:

  ```sql
  CREATE INDEX idx_blocks_started ON blocks(started_at DESC, id DESC);
  ```
- **Reason:** `block.list` with no filter is the TUI's history pane and `umb block list`, and
  without this index every page is a full scan and a sort of the whole table. The `id`
  column is in the index because it is the tie-breaker the cursor pages on, so the index
  serves the ordering whole rather than only its first column.
- **Migration 0001 rather than 0002:** nothing has been released, no database exists outside
  a developer's machine, and 0002 is reserved by T-F1-01 for the agent tables. Amending 0001
  keeps "one migration per subdomain" true; forward-only still holds, because there is no
  forward to be only in yet.

### specs/api/umbral-daemon-api-v1.md → §3 Cursor pagination, and §5.12 `block.search`

- **Before:** "Descending order by `started_at` or `created_at` unless stated otherwise."
- **After:** the same, and it is now stated otherwise for one method: `block.search` returns
  hits in **descending insertion order**, which is the order the daemon wrote the blocks and
  therefore the order their commands started. Its cursor carries that position rather than a
  timestamp.
- **Reason:** the measurement above. The two orderings differ only if rows were inserted out
  of time order, which the daemon never does: a block row is written when `OSC 133;C` opens
  it, so insertion order is start order by construction. What the client sees is the same
  list; what the daemon does to produce it is three hundred times cheaper.

## ADDED

### specs/data-model/umbral-schema.md → §2.4 `blocks_fts`, operational note

`blocks_fts` is an external-content table keyed on `blocks.rowid`, and `blocks` has a `TEXT`
primary key, so its rowids are implicit. SQLite's `VACUUM` may renumber implicit rowids,
which would silently desynchronise every row of the full-text index from the block it
describes and make `block.search` return the wrong blocks rather than fail.

THE SYSTEM SHALL NOT run `VACUUM` on the database without rebuilding the index afterwards:

```sql
INSERT INTO blocks_fts(blocks_fts) VALUES('rebuild');
```

This was already true before this delta; the ordering change only makes the dependency on
rowids explicit enough to be worth writing down. The retention job of T-F1-22 is the first
thing that will want to reclaim space, and it is the place to enforce this.

## Impact

| Artifact | Change |
|---|---|
| `specs/data-model/umbral-schema.md` | one `CREATE INDEX` in §2.2 and §5.1; an operational note in §2.4 |
| `specs/api/umbral-daemon-api-v1.md` | §3 gains the exception; §5.12 says what it orders by |
| `internal/store/migrations/0001_terminal.sql` | the index |

No requirement is added or removed, and no REQ text changes: REQ-BLK-006's 200 ms budget is
what this delta exists to meet.
