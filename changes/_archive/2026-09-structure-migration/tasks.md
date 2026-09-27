# Tasks — delta `2026-09-structure-migration`

### [x] 2026-09-20 T-SM-01 · Renumber the structure and agent migrations in the specs
- **What:** §2.4b-e and §5 of the Data Model, T-F0-14, T-F1-01 and the plan's F1 table.
- **REQ:** Art. 6
- **Files:** `specs/data-model/umbral-schema.md`, `specs/tasks/umbral-f0-tasks.md`, `specs/tasks/umbral-f1-tasks.md`, `specs/plans/umbral-mvp-plan.md`
- **Done:** `python3 tools/sdd_check.py` exits 0; no spec names `0001_terminal.sql` as a file a pending task writes to.
- **Result:** applied during the reconciliation of 2026-09-20. Ratified on 2026-09-20. It was the only change of that reconciliation that was not
  already an approved decision in one lineage or the other.

### [ ] T-SM-02 · Write `0003_structure.sql`
- **What:** the DDL itself, as part of T-F0-14.
- **REQ:** Art. 6
- **Depends on:** T-SM-01.
- **Done:** covered by T-F0-14's own criteria.
