# Tasks — delta `2026-09-art6-structural-ids`

### [x] 2026-09-20 T-ID-01 · Write the exception into Art. 6 and align the four artifacts
- **What:** amend Art. 6, then update API §3, Tech Design's Constitution check, Data Model §2
  conventions and §2.4b's `CHECK` constraints, and the PRD's Constitution check.
- **REQ:** Art. 6
- **Files:** `specs/constitution.md`, `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`, `specs/data-model/umbral-schema.md`, `specs/prd/umbral-mvp.md`
- **Done:** `python3 tools/sdd_check.py` exits 0 with the structure check inserting `w1`, `w1:t1`
  and `w1:p1` against the new constraints; no artifact asserts an unqualified ULID rule.
- **Result:** C-05 closed. The grammar is now enforced by the schema rather than only described,
  so T-F0-14's allocator cannot write a malformed identifier even by mistake.
