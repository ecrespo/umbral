# Tasks — delta `2026-09-cli-allowlist`

### [x] 2026-09-26 T-CAL-01 · Widen API §2's `cli` row to the workspace CLI
- **What:** API §2's `cli` row gains `workspace.*`, `tab.*`, `pane.*` except `pane.move`, and
  `layout.*`; API changelog 1.13. Tech Design §9.4 gains the tree commands' grammar (1.11). `T-F0-20` gains `internal/api/system.go` and its tests in
  its Files, and the traceability matrix gains the two tests of the delta's Verification.
- **REQ:** REQ-CLI-005
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`,
  `specs/tasks/umbral-f0-tasks.md`
- **Done:** `python3 tools/sdd_check.py` green; `task schema` green, since no method changes.
