# Tasks — delta `2026-09-cli-surface`

### [x] 2026-09-20 T-CLI-01 · Write the CLI surface and the package rows into the specs
- **What:** REQ-CLI-002's session rule and the new REQ-CLI-004; API Spec §5.17's `"last"`
  semantics; Tech Design §5.1 package list, §5.2 rows for `api`/`client`/`config`/`tui`,
  and §9.4 with the `umb` surface.
- **REQ:** REQ-CLI-002, REQ-CLI-004
- **Files:** `specs/prd/umbral-mvp.md`, `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`, `specs/tasks/umbral-f0-tasks.md`
- **Done:** `python3 tools/sdd_check.py` exits 0; no artifact cites a §5.2 row that does not exist.
- **Result:** applied during T-F0-11. Ratified on 2026-09-20.
