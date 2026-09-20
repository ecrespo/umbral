# Tasks — delta `2026-09-capability-degradation`

### [x] 2026-09-20 T-CD-01 · Say what a daemon advertises and what it refuses
- **What:** API Spec §2's `capabilities` bullet derives from the methods this build serves
  and excludes `system` and `api`; §9 states that an unserved method keeps its name so
  `NOT_IMPLEMENTED` is reachable; §5.37 describes `api.schema`'s result and says CI compares
  it with the specification.
- **REQ:** REQ-API-003, REQ-API-004
- **Files:** `specs/api/umbral-daemon-api-v1.md`
- **Done:** `python3 tools/sdd_check.py` and `python3 tools/api_schema_check.py` green; the
  four tests named in the delta's Verification section green.
