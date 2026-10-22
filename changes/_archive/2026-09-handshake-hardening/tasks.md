# Tasks — delta `2026-09-handshake-hardening`

### [ ] T-F1-32 · The handshake has a deadline and always answers
- **What:**
  - a 5 s deadline from accept to a completed `system.hello`, then `UNAUTHORIZED` with a null id
    and a close;
  - before the handshake, a JSON-RPC notification gets `UNAUTHORIZED` with a null id and a
    close;
  - a `system.hello` without an `id` or with a null `id` gets the same at any time, checked
    before any token;
  - API §1 (the JSON-RPC deviation), §2 and §8;
  - the ratification bookkeeping: T-F1-32 in the F1 tasks file and plan, and T-F1-01 depending
    on it.
- **REQ:** REQ-SEC-017, REQ-SEC-018
- **Files:** `internal/api/conn.go`, `internal/api/server.go`, `internal/api/server_test.go`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/prd/umbral-mvp.md`,
  `specs/tasks/umbral-f1-tasks.md`, `specs/plans/umbral-mvp-plan.md`
- **Depends on:** F0 complete. **Goes before T-F1-01.**
- **Done:** `TestASilentConnectionIsClosedAfterTheDeadline_REQ_SEC_017` and
  `TestANotificationBeforeHelloIsUnauthorized_REQ_SEC_018` green, each seen red against its own
  deliberate break; `task ci` green.
- **Estimate:** 0.5d

## Traceability matrix (delta)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-SEC-017 | T-F1-32 | TestASilentConnectionIsClosedAfterTheDeadline_REQ_SEC_017 |
| REQ-SEC-018 | T-F1-32 | TestANotificationBeforeHelloIsUnauthorized_REQ_SEC_018 |
