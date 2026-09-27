# Tasks — block query performance

### [x] 2026-09-11 T-BQP-01 · Fold the ordering and the index into the specs
- **What:** add `idx_blocks_started` to Data Model §2.2 and §5.1, the `VACUUM` note to §2.4,
  and the search-ordering exception to API Spec §3 and §5.12; bump both change histories.
- **REQ:** REQ-BLK-006
- **Files:** `specs/data-model/umbral-schema.md`, `specs/api/umbral-daemon-api-v1.md`,
  `internal/store/migrations/0001_terminal.sql`
- **Depends on:** approval of this delta
- **Done:** `BenchmarkBlockSearch100k_REQ_BLK_006` p95 under 200 ms; `python3 tools/sdd_check.py`
  exits 0, which re-executes the DDL and the migration-0001 insert.
- **Result:** Data Model v1.3 (`idx_blocks_started` in §2.2 and §5.1, the `VACUUM` rebuild rule
  in §2.4) and API Spec v1.4 (§3 names the exception, §5.12 states the ordering and what FTS5
  syntax means for a search box). Measured p95 after the change: search 0.93 ms, list page
  0.22 ms, against a 200 ms budget.

## Traceability matrix

| REQ | Task | Test |
|---|---|---|
| REQ-BLK-006 | T-BQP-01 | BenchmarkBlockSearch100k_REQ_BLK_006 |
