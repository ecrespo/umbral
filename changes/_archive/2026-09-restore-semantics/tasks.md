# Tasks — delta `2026-09-restore-semantics`

### [x] 2026-09-20 T-RS-01 · Settle what a restart restores and what it refuses to run
- **What:** Data Model §6 step 5 stops launching stored commands; `panes.command_pending` and
  the two focus columns; `pane_history` moves to `0004_restore` and the agent subdomain to
  `0005_agent`; API §4 `Pane` gains `command_pending` and §5.8 says its commands are pending;
  Tech Design specifies `$XDG_CONFIG_HOME/umbral/config.toml`.
- **REQ:** REQ-TERM-009, REQ-TERM-010, REQ-TERM-011
- **Files:** `specs/data-model/umbral-schema.md`, `specs/api/umbral-daemon-api-v1.md`,
  `specs/technical/umbral-architecture.md`, `specs/prd/umbral-mvp.md`
- **Done:** `python3 tools/sdd_check.py` and `python3 tools/api_schema_check.py` green; the six
  tests named in the delta's Verification section green.
