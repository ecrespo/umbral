# Proposal — Fixes for the 2026-09-11 Analyze findings (A-01…A-07)

> **Status:** draft
> **Affected base specs:** `specs/data-model/umbral-schema.md`, `specs/prd/umbral-mvp.md`, `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`, `specs/tasks/umbral-f0-tasks.md`, `specs/tasks/umbral-f1-tasks.md`, `tools/sdd_check.py`
> **Date:** 2026-09-11 · **Author:** Ernesto Crespo (assisted draft)
> **Evidence:** `specs/analyze/analyze-2026-09-11.md`

**Problem/motivation.**

| Finding | Severity | Summary | Consequence |
|---|---|---|---|
| A-01 | CRITICAL | Migration 0001 declares an FK to `threads`, which is created in 0002 | SQLite rejects every INSERT into `sessions` / `blocks` (verified); blocks T-F0-02 |
| A-02 | HIGH | The VT conformance suite has no enumerated cases | — |
| A-03 | HIGH | No defined behavior without a keyring | — |
| A-04 | HIGH | `thread.send` is not idempotent | — |
| A-05 | MEDIUM | `owner_thread_id` missing from the API | — |
| A-06 | MEDIUM | Drift `env_keyring_refs` / `env_refs_json` | — |
| A-07 | MEDIUM | FTS triggers not specified | — |

**Scope.**

- What changes: schema (migration order, FTS triggers, `client_msg_id`), two new REQs
  (REQ-SEC-008, REQ-AGT-015), an appendix with the VT conformance cases, two API fields and the checker.
- What does **not** change: the architectural styles, the constitution and the MVP scope.
- Left out, to be handled in another delta: A-08…A-15.

**Impact.**

- No pre-existing data (new project), so no data migration.
- API changes are additive only (`owner_thread_id`, `client_msg_id`); `protocol_version` stays at 1.
- Renaming `env_keyring_refs` to `env_refs` happens before any client exists, so there is no compatibility to break.

**Constitution check.**

- **Art. 5:** A-03 is solved **without** allowing `env:VAR` as a secret source; providers become disabled and visible. No amendment needed.
- **Art. 6:** `client_msg_id` is a ULID.
- **Art. 9:** it enters as a Delta.

**Proposed decision for A-01.** Create `threads` in migration 0001 with its full definition. It is
cheaper than rebuilding `sessions` and `blocks` in 0002, and `threads` depends on nothing. Verified:
with `threads` in 0001, INSERTs into `sessions` and `blocks` work with `foreign_keys=ON`.
