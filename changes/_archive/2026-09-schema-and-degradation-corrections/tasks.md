# Tasks — delta `2026-09-schema-and-degradation-corrections`

### [x] 2026-09-20 T-SDC-01 · Correct the capability rule and the published schema
- **What:** §9 scopes the registration rule to what a build implements and says when
  `METHOD_NOT_FOUND` is right; §9's capability bullet matches §2; §2's `cli` row grants
  `api.*`; `limit`, `cursor` and `include` become optional in §5.12/§5.13/§5.18; §5.37 states
  the JSON Schema dialect, the nullability spelling and the full `client_kinds` list;
  seventeen methods gain the request shapes they never had.
- **REQ:** REQ-API-003, REQ-API-004
- **Files:** `specs/api/umbral-daemon-api-v1.md`, `internal/api/**`, `tools/api_schema_check.py`
- **Done:** `python3 tools/api_schema_check.py` green with zero uncompared methods; the four
  tests named in the delta's Verification section green.
