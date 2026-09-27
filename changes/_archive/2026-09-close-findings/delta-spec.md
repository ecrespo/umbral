# Delta — Close findings (folded)

This delta was folded into `specs/` on 2026-09-20; it is kept as the record of what changed.

## ADDED

- PRD §6.6: REQ-SEC-008 (keyring unavailable), REQ-SEC-012 (environment fallback), REQ-SEC-013 to REQ-SEC-016 (rejection, key lifecycle, fail closed, offline recovery).
- PRD §6.3: REQ-AGT-015 (idempotency of `thread.send`), REQ-AGT-018 (`fetch_url` limits).
- PRD §6.11: REQ-AUT-005 to REQ-AUT-008 (limits, inventory, cancellation, stall detection).
- PRD §6.12: REQ-INT-006 (its own agent cannot be displaced).
- PRD §6.14: packaging area with REQ-PKG-001/002/003/006/008.
- PRD §6.15: REQ-OBS-004 (orchestration metrics).
- API: `client_msg_id`, `owner_thread_id`, `wait.list`, `wait.cancel`, the `rules.*` family, `thread.stalled`, `rules.update_rejected`, `CANCELLED`.
- Data Model: `trust_keys`, `rule_bundles`, `messages.client_msg_id` with its unique index, explicit FTS triggers.
- Tech Design: §8.1 (VT-01…VT-22), DD-016 (signing and recovery), DD-017 (wait observability).
- Tasks: T-F1-30, T-F1-31 and `specs/tasks/umbral-hardening-tasks.md` (T-PKG-01…03).

## MODIFIED

- Constitution Art. 5: environment fallback and signed rule material (two amendment rows).
- Data Model §5: `threads` moves to migration 0001 (A-01); 0002 gains `trust_keys` and `rule_bundles`.
- REQ-SEC-001: explicit entropy and length thresholds. REQ-CTX-001: precedence by depth and name.
- REQ-SEC-011: rewritten around signature, trust store and monotonic version.
- REQ-WS-006, REQ-TERM-010, REQ-TERM-011, REQ-API-004: ambiguities B-05, B-06, B-07 and B-10 closed.
- API §5.30: exactly one of `session_id` / `block_id`; `env_keyring_refs` renamed to `env_refs`.
- Tech Design §5.3: the agent's boundary is now called "write root".
- T-F0-02, T-F0-09, T-F0-14, T-F1-02, T-F1-09, T-F1-13, T-F1-18, T-F1-25: requirements and Done criteria extended.

## REMOVED

- REQ-PKG-004, REQ-PKG-005 and REQ-PKG-007 leave the MVP PRD and move to the future F2 PRD; their IDs stay reserved.
