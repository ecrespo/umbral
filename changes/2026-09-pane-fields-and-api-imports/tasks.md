# Tasks — delta `2026-09-pane-fields-and-api-imports`

### [ ] T-PF-01 · Write the two `Pane` fields and `api`'s domain imports into the specs
- **What:** API Spec §4's `Pane` gains `command` and `env`; Tech Design §5.2's `api` row
  admits the `domain` packages of the modules it serves; `.go-arch-lint.yml` narrows `api`
  to what it actually imports.
- **REQ:** REQ-WS-003, REQ-WS-005, Art. 3
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`, `.go-arch-lint.yml`
- **Done:** `task arch` green; an `api` → `agents/domain` import is rejected;
  `TestPaneCarriesItsCommandAndEnv` green.
