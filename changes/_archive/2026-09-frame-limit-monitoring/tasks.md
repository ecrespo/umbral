# Tasks — delta `2026-09-frame-limit-monitoring`

### [ ] T-F1-33 · The frame limit is enforced outbound, watched, and adjustable from the CLI
- **What:**
  - **Encoder:** every outbound frame is measured. A response over the limit becomes
    `RESULT_TOO_LARGE` (`-32014`, with `size_bytes` and `limit_bytes`). A notification over it
    becomes `limits.notification_dropped` under the same `seq`.
  - **`block.get`:** shortens its output to fit, on a UTF-8 boundary or a base64 boundary, and
    reports `output_response_truncated_bytes`.
  - **Monitoring:** `frames` counters in `system.status` and `limits.get`; warn logs without
    content.
  - **The setting:** `[api] max_message_bytes` (1–64 MiB, default 4) is the first live key.
    `limits.set` rewrites only that line, atomically, covering every case in the delta's
    file-cases table. The 4 MiB limit before the handshake is
    fixed. `max_message_bytes` is announced in `system.hello` and adopted by `internal/client`.
  - **CLI:** `umb limits` and `umb limits set --max-message <size>`, the `frames` line in
    `umb status`, and the hint on `RESULT_TOO_LARGE`.
  - **Spec edits:** API §1–§9, PRD, Tech §5.1, §7.2 and §9.4.
  - **Ratification bookkeeping:** T-F1-33 in the F1 tasks file and plan, and T-F1-01 depending
    on it.
- **REQ:** REQ-API-005, REQ-OBS-005, REQ-CLI-007
- **Files:** `internal/api/**`, `internal/client/**`, `internal/config/**`, `cmd/umb/**`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/prd/umbral-mvp.md`,
  `specs/technical/umbral-architecture.md`, `specs/tasks/umbral-f1-tasks.md`,
  `specs/plans/umbral-mvp-plan.md`
- **Depends on:** T-F1-32. **Goes before T-F1-01.**
- **Done:** the four tests of the delta's Verification are green, and each was seen red against
  its own break. `task schema` and `task ci` are green.
- **Estimate:** 3.5d

## Traceability matrix (delta)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-API-005 | T-F1-33 | TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005 |
| REQ-OBS-005 | T-F1-33 | TestFramesAreCounted_REQ_OBS_005 |
| REQ-CLI-007 | T-F1-33 | TestLimitsSetRaisesTheLimitForNewConnections_REQ_CLI_007, TestAnOversizedAnswerTellsTheUserHowToRaiseTheLimit_REQ_CLI_007 |
